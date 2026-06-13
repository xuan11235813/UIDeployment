package main

import (
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

// Node represents a single lidar device node
type Node struct {
	IP           string `json:"IP"`
	Username     string `json:"Username"`
	Password     string `json:"Password"`
	ExeDirectory string `json:"exeDirectory"`
	NodeID       string `json:"nodeId"`
	Description  string `json:"description"`
	Client       string `json:"client"`
}

// NodeGroup represents a group of nodes from a JSON file
type NodeGroup struct {
	Name  string `json:"name"`
	Nodes []Node `json:"nodes"`
}

// Config holds the application configuration
type Config struct {
	Groups []NodeGroup `json:"groups"`
}

var (
	upgrader = websocket.Upgrader{
		ReadBufferSize:  1024,
		WriteBufferSize: 1024,
		CheckOrigin: func(r *http.Request) bool {
			return true
		},
	}
	config     Config
	configLock sync.RWMutex
)

func main() {
	// Load configuration from JSON files
	if err := loadConfig(); err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	// Serve static files from executable-relative directory when possible
	staticDir := getStaticDir()
	log.Printf("Serving static files from %s", staticDir)
	fs := http.FileServer(http.Dir(staticDir))
	http.Handle("/static/", http.StripPrefix("/static/", fs))

	// Routes
	http.HandleFunc("/", indexHandler)
	http.HandleFunc("/api/groups", getGroupsHandler)
	http.HandleFunc("/api/ssh", sshHandler)
	http.HandleFunc("/api/upload", uploadHandler)
	http.HandleFunc("/api/download", downloadHandler)
	http.HandleFunc("/api/list", listFilesHandler)
	http.HandleFunc("/api/check-lidar", checkLidarHandler)
	http.HandleFunc("/api/lidar-ws", lidarWebSocketHandler)
	http.HandleFunc("/api/deploy", deployHandler)
	http.HandleFunc("/api/deploy-check", deployCheckHandler)
	http.HandleFunc("/api/deploy-config", deployConfigHandler)

	log.Println("Server starting on :8080...")
	log.Fatal(http.ListenAndServe(":8080", nil))
}

// loadConfig reads the JSON files and loads nodes into groups
func loadConfig() error {
	configLock.Lock()
	defer configLock.Unlock()

	config.Groups = nil

	// Load lidarpkx.json
	pkxNodes, err := loadNodesFromFile("lidarpkx.json")
	if err != nil {
		return fmt.Errorf("error loading lidarpkx.json: %v", err)
	}
	config.Groups = append(config.Groups, NodeGroup{
		Name:  "平可行 (PKX)",
		Nodes: pkxNodes,
	})

	// Load lidarxyt.json
	xytNodes, err := loadNodesFromFile("lidarxyt.json")
	if err != nil {
		return fmt.Errorf("error loading lidarxyt.json: %v", err)
	}
	config.Groups = append(config.Groups, NodeGroup{
		Name:  "新宇通 (XYT)",
		Nodes: xytNodes,
	})

	return nil
}

// getConfigPath tries to find the config file in multiple locations
func getConfigPath(filename string) (string, error) {
	// 1. Try current working directory
	if _, err := os.Stat(filename); err == nil {
		return filename, nil
	}

	// 2. Try executable directory
	execPath, err := os.Executable()
	if err == nil {
		execDir := filepath.Dir(execPath)
		configPath := filepath.Join(execDir, filename)
		if _, err := os.Stat(configPath); err == nil {
			return configPath, nil
		}
	}

	// 3. Try relative path from executable directory
	if err == nil {
		execDir := filepath.Dir(execPath)
		relPath := filepath.Join(execDir, "..", filename)
		if _, err := os.Stat(relPath); err == nil {
			return relPath, nil
		}
	}

	// 4. Try /home/pi/Desktop/lidarcontrol/ directory
	homePath := filepath.Join("/home/pi/Desktop/lidarcontrol", filename)
	if _, err := os.Stat(homePath); err == nil {
		return homePath, nil
	}

	return "", fmt.Errorf("config file %s not found in any search path", filename)
}

// deployHandler builds the remoteServer binary for the target node's arch,
// uploads it to the node, ensures /home/pi/lidarSystem exists, kills existing app, and starts it in a screen.
func deployHandler(w http.ResponseWriter, r *http.Request) {
	nodeID := r.URL.Query().Get("nodeId")
	if nodeID == "" {
		http.Error(w, "nodeId required", http.StatusBadRequest)
		return
	}

	// find node
	configLock.RLock()
	var targetNode *Node
	for _, group := range config.Groups {
		for _, node := range group.Nodes {
			if node.NodeID == nodeID {
				targetNode = &node
				break
			}
		}
		if targetNode != nil {
			break
		}
	}
	configLock.RUnlock()

	if targetNode == nil {
		http.Error(w, "Node not found", http.StatusNotFound)
		return
	}

	// connect via SSH to detect remote arch
	sshConfig := &ssh.ClientConfig{
		User:            targetNode.Username,
		Auth:            []ssh.AuthMethod{ssh.Password(targetNode.Password)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         10 * time.Second,
	}

	sshClient, err := ssh.Dial("tcp", targetNode.IP+":22", sshConfig)
	if err != nil {
		http.Error(w, fmt.Sprintf("SSH dial failed: %v", err), http.StatusInternalServerError)
		return
	}
	defer sshClient.Close()

	// run uname -m
	session, err := sshClient.NewSession()
	if err != nil {
		http.Error(w, fmt.Sprintf("SSH session failed: %v", err), http.StatusInternalServerError)
		return
	}
	out, err := session.CombinedOutput("uname -m")
	session.Close()
	if err != nil {
		http.Error(w, fmt.Sprintf("uname failed: %v", err), http.StatusInternalServerError)
		return
	}
	arch := strings.TrimSpace(string(out))

	// map arch to GOARCH/GOARM
	goEnv := map[string]string{"GOOS": "linux"}
	switch arch {
	case "x86_64", "amd64":
		goEnv["GOARCH"] = "amd64"
	case "aarch64", "arm64":
		goEnv["GOARCH"] = "arm64"
	case "armv7l", "armv7":
		goEnv["GOARCH"] = "arm"
		goEnv["GOARM"] = "7"
	case "i386", "i686":
		goEnv["GOARCH"] = "386"
	default:
		goEnv["GOARCH"] = "amd64"
	}

	// ensure upload dir exists
	cwd, _ := os.Getwd()
	uploadDir := filepath.Join(cwd, "upload")
	if err := os.MkdirAll(uploadDir, 0755); err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": fmt.Sprintf("failed to create upload dir: %v", err)})
		return
	}

	// build binary locally
	localBinary := filepath.Join(uploadDir, fmt.Sprintf("remoteServer-%s", goEnv["GOARCH"]))
	buildCmd := exec.Command("go", "build", "-o", localBinary, "../remote/remoteServer.go")

	// set env
	env := os.Environ()
	for k, v := range goEnv {
		env = append(env, fmt.Sprintf("%s=%s", k, v))
	}
	buildCmd.Env = env
	// set working dir so relative path resolves correctly
	if cwd != "" {
		buildCmd.Dir = cwd
	}
	buildOut, err := buildCmd.CombinedOutput()
	if err != nil {
		log.Printf("build failed: %v\noutput:\n%s", err, string(buildOut))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": fmt.Sprintf("build failed: %v", err), "output": string(buildOut)})
		return
	}

	// prepare remote: create dir, kill existing app, cleanup screens
	sess2, err := sshClient.NewSession()
	if err != nil {
		http.Error(w, fmt.Sprintf("session failed: %v", err), http.StatusInternalServerError)
		return
	}
	// command to create dir, kill old processes, and wipe screens
	cmd := `mkdir -p /home/pi/lidarSystem && pkill -f app.lexe || true && pkill -f remoteServer || true && screen -wipe || true`
	if err := sess2.Run(cmd); err != nil {
		// non-fatal — log and continue
		log.Printf("remote prep command returned: %v", err)
	}
	sess2.Close()

	// upload binary via SFTP
	sftpClient, err := sftp.NewClient(sshClient)
	if err != nil {
		http.Error(w, fmt.Sprintf("sftp client failed: %v", err), http.StatusInternalServerError)
		return
	}
	defer sftpClient.Close()

	remotePath := "/home/pi/lidarSystem/remoteServer"
	srcFile, err := os.Open(localBinary)
	if err != nil {
		http.Error(w, fmt.Sprintf("open local binary failed: %v", err), http.StatusInternalServerError)
		return
	}
	defer srcFile.Close()

	dstFile, err := sftpClient.Create(remotePath)
	if err != nil {
		http.Error(w, fmt.Sprintf("create remote file failed: %v", err), http.StatusInternalServerError)
		return
	}
	if _, err := io.Copy(dstFile, srcFile); err != nil {
		dstFile.Close()
		http.Error(w, fmt.Sprintf("upload failed: %v", err), http.StatusInternalServerError)
		return
	}
	dstFile.Close()

	// chmod +x
	if err := sftpClient.Chmod(remotePath, 0755); err != nil {
		log.Printf("chmod failed: %v", err)
	}

	// start in screen
	sess3, err := sshClient.NewSession()
	if err != nil {
		http.Error(w, fmt.Sprintf("session failed: %v", err), http.StatusInternalServerError)
		return
	}
	startCmd := fmt.Sprintf("cd /home/pi/lidarSystem && screen -S lidarServer -dm ./remoteServer > remoteServer.log 2>&1")
	if err := sess3.Run(startCmd); err != nil {
		sess3.Close()
		http.Error(w, fmt.Sprintf("failed to start remote server: %v", err), http.StatusInternalServerError)
		return
	}
	sess3.Close()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok", "arch": arch, "binary": localBinary})
}

// deployCheckHandler checks whether config.json exists on the remote node.
func deployCheckHandler(w http.ResponseWriter, r *http.Request) {
	nodeID := r.URL.Query().Get("nodeId")
	if nodeID == "" {
		http.Error(w, "nodeId required", http.StatusBadRequest)
		return
	}
	// find node
	configLock.RLock()
	var targetNode *Node
	for _, group := range config.Groups {
		for _, node := range group.Nodes {
			if node.NodeID == nodeID {
				targetNode = &node
				break
			}
		}
		if targetNode != nil {
			break
		}
	}
	configLock.RUnlock()
	if targetNode == nil {
		http.Error(w, "Node not found", http.StatusNotFound)
		return
	}

	sshConfig := &ssh.ClientConfig{
		User:            targetNode.Username,
		Auth:            []ssh.AuthMethod{ssh.Password(targetNode.Password)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         10 * time.Second,
	}
	sshClient, err := ssh.Dial("tcp", targetNode.IP+":22", sshConfig)
	if err != nil {
		http.Error(w, fmt.Sprintf("SSH dial failed: %v", err), http.StatusInternalServerError)
		return
	}
	defer sshClient.Close()

	// check file existence
	session, err := sshClient.NewSession()
	if err != nil {
		http.Error(w, fmt.Sprintf("session failed: %v", err), http.StatusInternalServerError)
		return
	}
	defer session.Close()
	cmd := "test -f /home/pi/lidarSystem/config.json && echo exists || echo missing"
	out, err := session.CombinedOutput(cmd)
	if err != nil {
		http.Error(w, fmt.Sprintf("check failed: %v", err), http.StatusInternalServerError)
		return
	}
	s := strings.TrimSpace(string(out))
	resp := map[string]interface{}{"configExists": false}
	if s == "exists" {
		resp["configExists"] = true
	} else {
		// load the local remote/config.json file as the default template
		if templatePath := getLocalRemoteConfigPath(); templatePath != "" {
			if data, err := os.ReadFile(templatePath); err == nil {
				var template interface{}
				if err := json.Unmarshal(data, &template); err == nil {
					resp["template"] = template
				}
			}
		}
		if resp["template"] == nil {
			resp["template"] = map[string]interface{}{
				"Server": map[string]string{"Port": "6008", "IpAddress": "0.0.0.0"},
				"Project": map[string]interface{}{"ProjectNum": 1, "ProjectName": "MyProject"},
				"LidarTypeVec": []map[string]interface{}{
					{"LidarID": "1", "Port": "6008", "IpAddress": "192.168.80.6", "LaneVec": []map[string]interface{}{{"LaneNum": 1, "LaneMinCoord": -4.0, "LaneMaxCoord": 4.0}}},
				},
			}
		}
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func getLocalRemoteConfigPath() string {
	paths := []string{}
	if cwd, err := os.Getwd(); err == nil {
		paths = append(paths, filepath.Join(cwd, "remote", "config.json"))
	}
	if execPath, err := os.Executable(); err == nil {
		execDir := filepath.Dir(execPath)
		paths = append(paths, filepath.Join(execDir, "remote", "config.json"), filepath.Join(execDir, "..", "remote", "config.json"))
	}
	paths = append(paths, filepath.Join("/home/pi/Desktop/lidarControl", "remote", "config.json"))
	for _, p := range paths {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

// deployConfigHandler writes provided config JSON to remote /home/pi/lidarSystem/config.json
func deployConfigHandler(w http.ResponseWriter, r *http.Request) {
	nodeID := r.URL.Query().Get("nodeId")
	if nodeID == "" {
		http.Error(w, "nodeId required", http.StatusBadRequest)
		return
	}
	// read body
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, fmt.Sprintf("read body failed: %v", err), http.StatusBadRequest)
		return
	}
	// validate JSON
	var v interface{}
	if err := json.Unmarshal(body, &v); err != nil {
		http.Error(w, fmt.Sprintf("invalid json: %v", err), http.StatusBadRequest)
		return
	}

	// find node
	configLock.RLock()
	var targetNode *Node
	for _, group := range config.Groups {
		for _, node := range group.Nodes {
			if node.NodeID == nodeID {
				targetNode = &node
				break
			}
		}
		if targetNode != nil {
			break
		}
	}
	configLock.RUnlock()
	if targetNode == nil {
		http.Error(w, "Node not found", http.StatusNotFound)
		return
	}

	sshConfig := &ssh.ClientConfig{
		User:            targetNode.Username,
		Auth:            []ssh.AuthMethod{ssh.Password(targetNode.Password)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         10 * time.Second,
	}
	sshClient, err := ssh.Dial("tcp", targetNode.IP+":22", sshConfig)
	if err != nil {
		http.Error(w, fmt.Sprintf("SSH dial failed: %v", err), http.StatusInternalServerError)
		return
	}
	defer sshClient.Close()

	// ensure dir exists
	sess, _ := sshClient.NewSession()
	_ = sess.Run("mkdir -p /home/pi/lidarSystem")
	sess.Close()

	// upload via sftp
	sftpClient, err := sftp.NewClient(sshClient)
	if err != nil {
		http.Error(w, fmt.Sprintf("sftp failed: %v", err), http.StatusInternalServerError)
		return
	}
	defer sftpClient.Close()

	f, err := sftpClient.Create("/home/pi/lidarSystem/config.json")
	if err != nil {
		http.Error(w, fmt.Sprintf("create failed: %v", err), http.StatusInternalServerError)
		return
	}
	if _, err := f.Write(body); err != nil {
		f.Close()
		http.Error(w, fmt.Sprintf("write failed: %v", err), http.StatusInternalServerError)
		return
	}
	f.Close()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// getStaticDir returns the directory that contains the static assets
func getStaticDir() string {
	cwd, err := os.Getwd()
	if err == nil {
		staticPath := filepath.Join(cwd, "static")
		if _, err := os.Stat(staticPath); err == nil {
			return staticPath
		}
	}

	execPath, err := os.Executable()
	if err == nil {
		execDir := filepath.Dir(execPath)
		staticPath := filepath.Join(execDir, "static")
		if _, err := os.Stat(staticPath); err == nil {
			return staticPath
		}

		parentStaticPath := filepath.Join(execDir, "..", "static")
		if _, err := os.Stat(parentStaticPath); err == nil {
			return parentStaticPath
		}
	}

	// fallback to well-known project path
	projectStaticPath := filepath.Join("/home/pi/Desktop/lidarcontrol", "static")
	if _, err := os.Stat(projectStaticPath); err == nil {
		return projectStaticPath
	}

	// fallback to relative path
	return "./static"
}

// loadNodesFromFile reads nodes from a JSON file
func loadNodesFromFile(filename string) ([]Node, error) {
	configPath, err := getConfigPath(filename)
	if err != nil {
		return nil, err
	}

	file, err := os.Open(configPath)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	var data struct {
		Nodes []Node `json:"Nodes"`
	}

	decoder := json.NewDecoder(file)
	if err := decoder.Decode(&data); err != nil {
		return nil, err
	}

	return data.Nodes, nil
}

// indexHandler serves the main HTML page
func indexHandler(w http.ResponseWriter, r *http.Request) {
	tmpl, err := template.New("index").Parse(indexHTML)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := tmpl.Execute(w, nil); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// getGroupsHandler returns the node groups as JSON
func getGroupsHandler(w http.ResponseWriter, r *http.Request) {
	configLock.RLock()
	defer configLock.RUnlock()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(config.Groups)
}

// sshHandler handles SSH connections via WebSocket
func sshHandler(w http.ResponseWriter, r *http.Request) {
	nodeID := r.URL.Query().Get("nodeId")
	if nodeID == "" {
		http.Error(w, "nodeId parameter required", http.StatusBadRequest)
		return
	}

	// Find the node
	configLock.RLock()
	var targetNode *Node
	for _, group := range config.Groups {
		for _, node := range group.Nodes {
			if node.NodeID == nodeID {
				targetNode = &node
				break
			}
		}
		if targetNode != nil {
			break
		}
	}
	configLock.RUnlock()

	if targetNode == nil {
		http.Error(w, "Node not found", http.StatusNotFound)
		return
	}

	// Upgrade to WebSocket
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("WebSocket upgrade error: %v", err)
		return
	}
	defer conn.Close()

	// Connect to SSH
	sshConfig := &ssh.ClientConfig{
		User: targetNode.Username,
		Auth: []ssh.AuthMethod{
			ssh.Password(targetNode.Password),
		},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         10 * time.Second,
	}

	client, err := ssh.Dial("tcp", targetNode.IP+":22", sshConfig)
	if err != nil {
		conn.WriteMessage(websocket.TextMessage, []byte(fmt.Sprintf("SSH connection failed: %v\r\n", err)))
		return
	}
	defer client.Close()

	// Create session
	session, err := client.NewSession()
	if err != nil {
		conn.WriteMessage(websocket.TextMessage, []byte(fmt.Sprintf("Session creation failed: %v\r\n", err)))
		return
	}
	defer session.Close()

	// Set up terminal modes
	modes := ssh.TerminalModes{
		ssh.ECHO:          1,
		ssh.TTY_OP_ISPEED: 14400,
		ssh.TTY_OP_OSPEED: 14400,
	}

	// Request pseudo terminal with larger size and xterm-256color
	if err := session.RequestPty("xterm-256color", 40, 160, modes); err != nil {
		conn.WriteMessage(websocket.TextMessage, []byte(fmt.Sprintf("PTY request failed: %v\r\n", err)))
		return
	}

	// Set up pipes
	stdin, _ := session.StdinPipe()
	stdout, _ := session.StdoutPipe()
	stderr, _ := session.StderrPipe()

	// Start shell
	if err := session.Shell(); err != nil {
		conn.WriteMessage(websocket.TextMessage, []byte(fmt.Sprintf("Shell start failed: %v\r\n", err)))
		return
	}

	// Handle bidirectional communication
	done := make(chan bool)

	// Read from SSH and send to WebSocket
	go func() {
		buf := make([]byte, 1024)
		for {
			n, err := stdout.Read(buf)
			if err != nil {
				done <- true
				return
			}
			conn.WriteMessage(websocket.TextMessage, buf[:n])
		}
	}()

	// Read from stderr and send to WebSocket
	go func() {
		buf := make([]byte, 1024)
		for {
			n, err := stderr.Read(buf)
			if err != nil {
				return
			}
			conn.WriteMessage(websocket.TextMessage, buf[:n])
		}
	}()

	// Read from WebSocket and send to SSH
	go func() {
		for {
			_, msg, err := conn.ReadMessage()
			if err != nil {
				done <- true
				return
			}
			stdin.Write(msg)
		}
	}()

	<-done
}

// uploadHandler handles file uploads to edge box via SFTP
func uploadHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	if r.Method != http.MethodPost {
		json.NewEncoder(w).Encode(map[string]string{
			"error": "Method not allowed",
		})
		return
	}

	nodeID := r.FormValue("nodeId")
	remotePath := r.FormValue("remotePath")
	if nodeID == "" || remotePath == "" {
		json.NewEncoder(w).Encode(map[string]string{
			"error": "nodeId and remotePath parameters required",
		})
		return
	}

	// Find the node
	configLock.RLock()
	var targetNode *Node
	for _, group := range config.Groups {
		for _, node := range group.Nodes {
			if node.NodeID == nodeID {
				targetNode = &node
				break
			}
		}
		if targetNode != nil {
			break
		}
	}
	configLock.RUnlock()

	if targetNode == nil {
		json.NewEncoder(w).Encode(map[string]string{
			"error": "Node not found",
		})
		return
	}

	// Get uploaded file
	file, header, err := r.FormFile("file")
	if err != nil {
		json.NewEncoder(w).Encode(map[string]string{
			"error": fmt.Sprintf("Error getting file: %v", err),
		})
		return
	}
	defer file.Close()

	// Connect to SSH
	sshConfig := &ssh.ClientConfig{
		User: targetNode.Username,
		Auth: []ssh.AuthMethod{
			ssh.Password(targetNode.Password),
		},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         10 * time.Second,
	}

	client, err := ssh.Dial("tcp", targetNode.IP+":22", sshConfig)
	if err != nil {
		json.NewEncoder(w).Encode(map[string]string{
			"error": fmt.Sprintf("SSH connection failed: %v", err),
		})
		return
	}
	defer client.Close()

	// Create SFTP client
	sftpClient, err := sftp.NewClient(client)
	if err != nil {
		json.NewEncoder(w).Encode(map[string]string{
			"error": fmt.Sprintf("SFTP client creation failed: %v", err),
		})
		return
	}
	defer sftpClient.Close()

	// Expand home directory if needed
	actualPath := remotePath
	if strings.HasPrefix(remotePath, "~") {
		homeDir, err := sftpClient.Getwd()
		if err == nil {
			if remotePath == "~" {
				actualPath = homeDir
			} else {
				actualPath = strings.Replace(remotePath, "~", homeDir, 1)
			}
		}
	}

	// Create remote file
	remoteFile, err := sftpClient.Create(filepath.Join(actualPath, header.Filename))
	if err != nil {
		json.NewEncoder(w).Encode(map[string]string{
			"error": fmt.Sprintf("Remote file creation failed: %v", err),
		})
		return
	}
	defer remoteFile.Close()

	// Copy file content
	_, err = io.Copy(remoteFile, file)
	if err != nil {
		json.NewEncoder(w).Encode(map[string]string{
			"error": fmt.Sprintf("File transfer failed: %v", err),
		})
		return
	}

	json.NewEncoder(w).Encode(map[string]string{
		"status":  "success",
		"message": fmt.Sprintf("File %s uploaded successfully", header.Filename),
	})
}

// downloadHandler handles file downloads from edge box via SFTP
func downloadHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"error": "Method not allowed"})
		return
	}

	nodeID := r.URL.Query().Get("nodeId")
	remotePath := r.URL.Query().Get("remotePath")
	if nodeID == "" || remotePath == "" {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"error": "nodeId and remotePath parameters required"})
		return
	}

	// Find the node
	configLock.RLock()
	var targetNode *Node
	for _, group := range config.Groups {
		for _, node := range group.Nodes {
			if node.NodeID == nodeID {
				targetNode = &node
				break
			}
		}
		if targetNode != nil {
			break
		}
	}
	configLock.RUnlock()

	if targetNode == nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"error": "Node not found"})
		return
	}

	// Connect to SSH
	sshConfig := &ssh.ClientConfig{
		User: targetNode.Username,
		Auth: []ssh.AuthMethod{
			ssh.Password(targetNode.Password),
		},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         10 * time.Second,
	}

	client, err := ssh.Dial("tcp", targetNode.IP+":22", sshConfig)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"error": fmt.Sprintf("SSH connection failed: %v", err)})
		return
	}
	defer client.Close()

	// Create SFTP client
	sftpClient, err := sftp.NewClient(client)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"error": fmt.Sprintf("SFTP client creation failed: %v", err)})
		return
	}
	defer sftpClient.Close()

	// Expand home directory if needed
	actualPath := remotePath
	if strings.HasPrefix(remotePath, "~") {
		homeDir, err := sftpClient.Getwd()
		if err == nil {
			if remotePath == "~" {
				actualPath = homeDir
			} else {
				actualPath = strings.Replace(remotePath, "~", homeDir, 1)
			}
		}
	}

	// Open remote file
	remoteFile, err := sftpClient.Open(actualPath)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"error": fmt.Sprintf("Remote file open failed: %v", err)})
		return
	}
	defer remoteFile.Close()

	// Get file info for Content-Disposition
	fileInfo, err := remoteFile.Stat()
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"error": fmt.Sprintf("File stat failed: %v", err)})
		return
	}

	// Set response headers
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%s", fileInfo.Name()))
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", fmt.Sprintf("%d", fileInfo.Size()))

	// Stream file to response
	io.Copy(w, remoteFile)
}

// listFilesHandler lists files in a directory on the edge box
func listFilesHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	nodeID := r.URL.Query().Get("nodeId")
	path := r.URL.Query().Get("path")
	if nodeID == "" {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": "nodeId parameter required",
		})
		return
	}

	if path == "" {
		path = "~"
	}

	// Find the node
	configLock.RLock()
	var targetNode *Node
	for _, group := range config.Groups {
		for _, node := range group.Nodes {
			if node.NodeID == nodeID {
				targetNode = &node
				break
			}
		}
		if targetNode != nil {
			break
		}
	}
	configLock.RUnlock()

	if targetNode == nil {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": "Node not found",
		})
		return
	}

	// Connect to SSH
	sshConfig := &ssh.ClientConfig{
		User: targetNode.Username,
		Auth: []ssh.AuthMethod{
			ssh.Password(targetNode.Password),
		},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         10 * time.Second,
	}

	client, err := ssh.Dial("tcp", targetNode.IP+":22", sshConfig)
	if err != nil {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": fmt.Sprintf("SSH connection failed: %v", err),
		})
		return
	}
	defer client.Close()

	// Create SFTP client
	sftpClient, err := sftp.NewClient(client)
	if err != nil {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": fmt.Sprintf("SFTP client creation failed: %v", err),
		})
		return
	}
	defer sftpClient.Close()

	// Expand home directory if needed
	actualPath := path
	if strings.HasPrefix(path, "~") {
		// Get the home directory from SFTP
		homeDir, err := sftpClient.Getwd()
		if err == nil {
			// Replace ~ with actual home path
			if path == "~" {
				actualPath = homeDir
			} else {
				actualPath = strings.Replace(path, "~", homeDir, 1)
			}
		}
	}

	// List files
	files, err := sftpClient.ReadDir(actualPath)
	if err != nil {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"error": fmt.Sprintf("Directory read failed: %v", err),
			"path":  actualPath,
		})
		return
	}

	// Build file list
	fileList := make([]map[string]interface{}, 0)
	for _, f := range files {
		fileList = append(fileList, map[string]interface{}{
			"name":  f.Name(),
			"size":  f.Size(),
			"isDir": f.IsDir(),
			"mode":  f.Mode().String(),
			"mtime": f.ModTime().Format("2006-01-02 15:04:05"),
		})
	}

	json.NewEncoder(w).Encode(map[string]interface{}{
		"path":  actualPath,
		"files": fileList,
	})
}

// checkLidarHandler checks connectivity to LiDAR devices from the edge box
func checkLidarHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	nodeID := r.URL.Query().Get("nodeId")
	lidarIP := r.URL.Query().Get("lidarIP")

	if nodeID == "" || lidarIP == "" {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"error":   "nodeId and lidarIP parameters required",
		})
		return
	}

	// Find the node
	configLock.RLock()
	var targetNode *Node
	for _, group := range config.Groups {
		for _, node := range group.Nodes {
			if node.NodeID == nodeID {
				targetNode = &node
				break
			}
		}
		if targetNode != nil {
			break
		}
	}
	configLock.RUnlock()

	if targetNode == nil {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"error":   "Node not found",
		})
		return
	}

	// Connect to SSH on the edge box
	sshConfig := &ssh.ClientConfig{
		User: targetNode.Username,
		Auth: []ssh.AuthMethod{
			ssh.Password(targetNode.Password),
		},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         10 * time.Second,
	}

	client, err := ssh.Dial("tcp", targetNode.IP+":22", sshConfig)
	if err != nil {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("SSH connection to edge box failed: %v", err),
		})
		return
	}
	defer client.Close()

	// Create session to run ping command
	session, err := client.NewSession()
	if err != nil {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("Session creation failed: %v", err),
		})
		return
	}
	defer session.Close()

	// Use ping to check connectivity (send 1 packet, timeout 2 seconds)
	cmd := fmt.Sprintf("ping -c 1 -W 2 %s", lidarIP)
	err = session.Run(cmd)
	if err != nil {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success": false,
			"error":   fmt.Sprintf("LiDAR %s is not reachable", lidarIP),
		})
		return
	}

	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"message": fmt.Sprintf("LiDAR %s is reachable", lidarIP),
	})
}

// lidarWebSocketHandler proxies WebSocket connections to LiDAR data stream on edge box
func lidarWebSocketHandler(w http.ResponseWriter, r *http.Request) {
	nodeID := r.URL.Query().Get("nodeId")
	lidarPort := r.URL.Query().Get("lidarPort")

	if nodeID == "" || lidarPort == "" {
		http.Error(w, "nodeId and lidarPort parameters required", http.StatusBadRequest)
		return
	}

	// Find the node
	configLock.RLock()
	var targetNode *Node
	for _, group := range config.Groups {
		for _, node := range group.Nodes {
			if node.NodeID == nodeID {
				targetNode = &node
				break
			}
		}
		if targetNode != nil {
			break
		}
	}
	configLock.RUnlock()

	if targetNode == nil {
		http.Error(w, "Node not found", http.StatusNotFound)
		return
	}

	// Upgrade client connection to WebSocket
	clientConn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("Client WebSocket upgrade error: %v", err)
		return
	}
	defer clientConn.Close()

	// Connect to SSH on the edge box to establish WebSocket tunnel
	sshConfig := &ssh.ClientConfig{
		User: targetNode.Username,
		Auth: []ssh.AuthMethod{
			ssh.Password(targetNode.Password),
		},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         10 * time.Second,
	}

	sshClient, err := ssh.Dial("tcp", targetNode.IP+":22", sshConfig)
	if err != nil {
		clientConn.WriteMessage(websocket.TextMessage, []byte(fmt.Sprintf("SSH connection failed: %v", err)))
		return
	}
	defer sshClient.Close()

	// Attempt to connect directly to remote node's WebSocket server at /ws
	// remote server uses base Port + LidarID to avoid port conflicts when multiple lidars share the same base port
	lidarID := r.URL.Query().Get("lidarId")
	var remotePort int
	basePort, err := strconv.Atoi(lidarPort)
	if err != nil {
		clientConn.WriteMessage(websocket.TextMessage, []byte(fmt.Sprintf("invalid lidarPort: %v", err)))
		return
	}
	idNum := 0
	if lidarID != "" {
		idNum, _ = strconv.Atoi(lidarID)
	}
	remotePort = basePort + idNum
	remoteWSURL := fmt.Sprintf("ws://%s:%d/ws", targetNode.IP, remotePort)
	dialer := websocket.Dialer{
		Proxy:            http.ProxyFromEnvironment,
		HandshakeTimeout: 10 * time.Second,
	}

	remoteConn, resp, err := dialer.Dial(remoteWSURL, nil)
	if err != nil {
		// If dial fails, inform the client and return
		log.Printf("Failed to dial remote websocket %s: %v", remoteWSURL, err)
		if resp != nil {
			log.Printf("Remote response status: %s", resp.Status)
		}
		clientConn.WriteMessage(websocket.TextMessage, []byte(fmt.Sprintf("Failed to connect to remote LiDAR websocket: %v", err)))
		return
	}
	defer remoteConn.Close()

	// Proxy messages: remote -> client and client -> remote
	proxyDone := make(chan struct{})

	// remote -> client
	go func() {
		defer func() { close(proxyDone) }()
		for {
			mt, msg, err := remoteConn.ReadMessage()
			if err != nil {
				log.Printf("remote read error: %v", err)
				return
			}
			if err := clientConn.WriteMessage(mt, msg); err != nil {
				log.Printf("client write error: %v", err)
				return
			}
		}
	}()

	// client -> remote (in case front-end sends control messages)
	go func() {
		for {
			mt, msg, err := clientConn.ReadMessage()
			if err != nil {
				// client disconnected or read error
				remoteConn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
				return
			}
			if err := remoteConn.WriteMessage(mt, msg); err != nil {
				log.Printf("remote write error: %v", err)
				return
			}
		}
	}()

	// Wait until proxying finishes (either direction closed)
	<-proxyDone
}

// Point2D represents a 2D point for LiDAR data
type Point2D struct {
	X float64
	Y float64
	R float64
}

// generateSimulatedLidarData creates simulated LiDAR point cloud data
func generateSimulatedLidarData(frameIndex int) []Point2D {
	// Generate approximately 360 points per frame (similar to real LiDAR)
	// Range: X: [-20, 20], Y: [-10, 10]
	var points []Point2D

	numPoints := 360
	angleStep := 360.0 / float64(numPoints)

	// Simulate a road scene with some vehicles
	for i := 0; i < numPoints; i++ {
		angle := float64(i) * angleStep
		rad := angle * 3.141592653589793 / 180.0

		// Base distance varies to simulate road surface
		baseDistance := 15.0 + 5.0*math.Sin(rad*2) // Road surface pattern

		// Add some "vehicles" as obstacles
		// Vehicle 1: around angle 45-60 degrees
		if angle >= 45 && angle <= 60 {
			baseDistance = 5.0 + 2.0*math.Sin(rad*3) // Closer object (vehicle)
		}
		// Vehicle 2: around angle 120-140 degrees
		if angle >= 120 && angle <= 140 {
			baseDistance = 8.0 + 1.5*math.Sin(rad*2) // Another vehicle
		}
		// Vehicle 3: around angle 200-220 degrees
		if angle >= 200 && angle <= 220 {
			baseDistance = 6.0 + 3.0*math.Cos(rad*4) // Third vehicle
		}

		// Add some noise for realism
		noise := 0.1 * (math.Sin(float64(frameIndex)*0.1+float64(i)*0.05) + 0.5*math.Cos(float64(frameIndex)*0.2))

		distance := baseDistance + noise

		// Calculate X, Y coordinates
		// X: horizontal (left-right), Y: forward direction
		x := distance * math.Sin(rad)
		y := distance * math.Cos(rad)

		// Clamp to specified range: X: [-20, 20], Y: [-10, 10]
		if x < -20 {
			x = -20
		} else if x > 20 {
			x = 20
		}
		if y < -10 {
			y = -10
		} else if y > 10 {
			y = 10
		}

		// Only include points within valid range
		if distance > 0 && distance < 20 {
			points = append(points, Point2D{
				X: x,
				Y: y,
				R: distance,
			})
		}
	}

	return points
}

// indexHTML is the embedded HTML template
const indexHTML = `<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>Lidar Control Panel</title>
    <link rel="stylesheet" href="/static/css/style.css">
    <link rel="stylesheet" href="https://cdn.jsdelivr.net/npm/xterm@5.3.0/css/xterm.min.css">
</head>
<body>
    <div class="container">
        <header>
            <h1> Lidar Control Panel</h1>
            <p>Traffic Department Monitoring Center</p>
        </header>

        <div class="main-content">
            <!-- Column 1: Node Groups (fixed width) -->
            <div class="sidebar">
                <h2>Node Groups</h2>
                <div id="groups-container"></div>
            </div>

            <!-- Column 2: Node Operations with Tabs -->
            <div class="right-panel">
                <!-- Tab Navigation -->
                <div class="tab-navigation">
                    <button class="tab-btn active" onclick="switchTab('terminal-file-tab')" id="tab-btn-terminal">SSH Terminal & File Manager</button>
                    <button class="tab-btn" onclick="switchTab('lidar-tab')" id="tab-btn-lidar">LiDAR Operation</button>
                </div>

                <!-- Tab 1: SSH Terminal and File Manager -->
                <div id="terminal-file-tab" class="tab-content active">
                    <div id="terminal-panel" class="panel fixed-height">
                        <div class="panel-header">
                            <h2>SSH Terminal - <span id="terminal-node-name">Select a node</span></h2>
                        </div>
                        <div id="terminal-container"></div>
                    </div>

                    <div id="file-panel" class="panel fixed-height">
                        <div class="panel-header">
                            <h2>File Manager - <span id="file-node-name">Select a node</span></h2>
                        </div>
                        
                        <div class="file-manager">
                            <div class="file-browser">
                                <div class="path-bar">
                                    <input type="text" id="current-path" value="~" placeholder="Enter path...">
                                    <button onclick="browsePath()" class="btn btn-primary">Go</button>
                                    <button onclick="goParent()" class="btn btn-secondary">↑ Parent</button>
                                </div>
                                <div id="file-list"></div>
                            </div>
                            
                            <div class="file-actions">
                                <h3>Upload File</h3>
                                <form id="upload-form" enctype="multipart/form-data">
                                    <input type="file" id="file-input" name="file" required>
                                    <input type="hidden" id="upload-node-id">
                                    <input type="hidden" id="upload-path">
                                    <button type="submit" class="btn btn-success">Upload</button>
                                </form>
                                <div id="upload-status"></div>
                            </div>
                        </div>
                    </div>
                </div>

                <!-- Tab 2: LiDAR Operation -->
                <div id="lidar-tab" class="tab-content">
                    <div id="lidar-panel" class="panel">
                        <div class="panel-header">
                            <h2>LiDAR Connectivity Check - <span id="lidar-node-name">Select a node</span></h2>
                        </div>
                        <div class="lidar-check-container">
                            <div class="lidar-info">
                                <p>Test connectivity from edge box to LiDAR devices (192.168.80.x network)</p>
                            </div>
                            <div class="lidar-buttons">
								<button id="deploy-btn" class="lidar-btn lidar-btn-deploy" onclick="deployWithConfigCheck()">
									<span class="lidar-btn-icon">⬆️</span>
									<span class="lidar-btn-label">Deploy</span>
								</button>
								<button id="lidar-btn-1" class="lidar-btn lidar-btn-gray" onclick="checkLidar('192.168.80.6', 1, '6008')">
                                    <span class="lidar-btn-icon">📡</span>
                                    <span class="lidar-btn-label">LiDAR 1</span>
                                    <span class="lidar-btn-ip">192.168.80.6</span>
                                </button>
                                <button id="lidar-btn-2" class="lidar-btn lidar-btn-gray" onclick="checkLidar('192.168.80.7', 2, '6008')">
                                    <span class="lidar-btn-icon">📡</span>
                                    <span class="lidar-btn-label">LiDAR 2</span>
                                    <span class="lidar-btn-ip">192.168.80.7</span>
                                </button>
                                <button id="lidar-btn-3" class="lidar-btn lidar-btn-gray" onclick="checkLidar('192.168.80.8', 3, '6008')">
                                    <span class="lidar-btn-icon">📡</span>
                                    <span class="lidar-btn-label">LiDAR 3</span>
                                    <span class="lidar-btn-ip">192.168.80.8</span>
                                </button>
                            </div>
                            <div id="lidar-status" class="lidar-status"></div>
                        </div>
                    </div>

                    <!-- LiDAR Data Visualization Panel -->
                    <div id="lidar-visualization-panel" class="panel">
                        <div class="panel-header">
                            <h2>LiDAR Point Cloud Visualization - <span id="lidar-visualization-title">No LiDAR selected</span></h2>
                            <div class="lidar-visualization-controls">
                                <button id="lidar-stop-btn" class="btn btn-danger" onclick="stopLidarVisualization()" style="display: none;">Stop</button>
                                <span id="lidar-frame-counter" class="lidar-frame-counter">Frames: 0</span>
                            </div>
                        </div>
                        <div class="lidar-canvas-container">
                            <canvas id="lidar-canvas" width="800" height="400"></canvas>
                        </div>
                        <div class="lidar-canvas-info">
                            <span>Range: X [-20, 20] m | Y [-10, 10] m</span>
                            <span id="lidar-point-count">Points: 0</span>
                        </div>
                    </div>
                </div>
            </div>
        </div>
    </div>

    <script src="https://cdn.jsdelivr.net/npm/xterm@5.3.0/lib/xterm.min.js"></script>
    <script src="https://cdn.jsdelivr.net/npm/xterm-addon-fit@0.8.0/lib/xterm-addon-fit.min.js"></script>
    <script src="https://cdn.jsdelivr.net/npm/xterm-addon-web-links@0.9.0/lib/xterm-addon-web-links.min.js"></script>
	<!-- Configuration modal for editing remote config.json -->
	<div id="config-modal" class="modal" style="display:none;">
		<div class="modal-content">
			<div class="modal-header">
				<h3>Edit Remote config.json</h3>
				<button id="config-modal-close" class="btn btn-secondary">✖</button>
			</div>
			<div class="modal-body">
				<form id="config-form">
					<div class="config-section">
						<h4>Server</h4>
						<label>Port <input id="server-port" type="text" placeholder="6008"></label>
						<label>IP Address <input id="server-ip" type="text" placeholder="0.0.0.0"></label>
					</div>
					<div class="config-section">
						<h4>Project</h4>
						<label>Project Number <input id="project-num" type="number" min="1"></label>
						<label>Project Name <input id="project-name" type="text" placeholder="MyProject"></label>
					</div>
					<div class="config-section">
						<div class="section-heading">
							<h4>LiDAR Devices</h4>
							<button id="add-lidar" type="button" class="btn btn-success">Add LiDAR</button>
						</div>
						<div id="lidar-list" class="lidar-list"></div>
					</div>
				</form>
				<div class="modal-actions">
					<button id="config-save" type="button" class="btn btn-success">Save & Upload</button>
					<button id="config-cancel" type="button" class="btn btn-secondary">Cancel</button>
				</div>
			</div>
		</div>
	</div>
	<script src="/static/js/app.js"></script>
</body>
</html>
`
