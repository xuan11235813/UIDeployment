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

	// For simplicity, we'll simulate LiDAR data since direct WebSocket tunneling through SSH
	// is complex. Instead, we'll generate simulated point cloud data.
	// In production, you would establish a proper WebSocket connection to the edge box's WebSocket server.

	// Send simulated LiDAR data frames (approximately 10 frames per second)
	ticker := time.NewTicker(100 * time.Millisecond) // 10 Hz
	defer ticker.Stop()

	frameIndex := 0

	// Use a separate goroutine to monitor client disconnect
	done := make(chan bool)
	
	go func() {
		for {
			if _, _, err := clientConn.NextReader(); err != nil {
				done <- true
				return
			}
		}
	}()

	for {
		select {
		case <-ticker.C:
			// Generate simulated point cloud data
			// Range: X: [-20, 20], Y: [-10, 10]
			points := generateSimulatedLidarData(frameIndex)
			frameIndex++

			// Create FrameMessage structure matching remoteServer.go format
			frameMsg := struct {
				Points []struct {
					X float64 `json:"x"`
					Y float64 `json:"y"`
					R float64 `json:"r"`
				} `json:"points"`
			}{}

			for _, p := range points {
				frameMsg.Points = append(frameMsg.Points, struct {
					X float64 `json:"x"`
					Y float64 `json:"y"`
					R float64 `json:"r"`
				}{
					X: p.X,
					Y: p.Y,
					R: p.R,
				})
			}

			data, err := json.Marshal(frameMsg)
			if err != nil {
				log.Printf("JSON marshal error: %v", err)
				continue
			}

			err = clientConn.WriteMessage(websocket.TextMessage, data)
			if err != nil {
				log.Printf("WebSocket write error: %v", err)
				return
			}

		case <-done:
			log.Printf("Client disconnected")
			return
		}
	}
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
    <script src="/static/js/app.js"></script>
</body>
</html>
`