package main

import (
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
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

	// Serve static files
	fs := http.FileServer(http.Dir("./static"))
	http.Handle("/static/", http.StripPrefix("/static/", fs))

	// Routes
	http.HandleFunc("/", indexHandler)
	http.HandleFunc("/api/groups", getGroupsHandler)
	http.HandleFunc("/api/ssh", sshHandler)
	http.HandleFunc("/api/upload", uploadHandler)
	http.HandleFunc("/api/download", downloadHandler)
	http.HandleFunc("/api/list", listFilesHandler)

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

// loadNodesFromFile reads nodes from a JSON file
func loadNodesFromFile(filename string) ([]Node, error) {
	file, err := os.Open(filename)
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
            <h1>🚗 Lidar Control Panel</h1>
            <p>Traffic Department Monitoring Center</p>
        </header>

        <div class="main-content">
            <div class="sidebar">
                <h2>Node Groups</h2>
                <div id="groups-container"></div>
            </div>

            <div class="right-panel">
                <div id="terminal-panel" class="panel">
                    <div class="panel-header">
                        <h2>SSH Terminal - <span id="terminal-node-name">Select a node</span></h2>
                    </div>
                    <div id="terminal-container"></div>
                </div>

                <div id="file-panel" class="panel">
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
        </div>
    </div>

    <script src="https://cdn.jsdelivr.net/npm/xterm@5.3.0/lib/xterm.min.js"></script>
    <script src="https://cdn.jsdelivr.net/npm/xterm-addon-fit@0.8.0/lib/xterm-addon-fit.min.js"></script>
    <script src="https://cdn.jsdelivr.net/npm/xterm-addon-web-links@0.9.0/lib/xterm-addon-web-links.min.js"></script>
    <script src="/static/js/app.js"></script>
</body>
</html>
`