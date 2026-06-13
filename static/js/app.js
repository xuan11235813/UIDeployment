// Global state
let groups = [];
let currentNode = null;
let terminalSocket = null;
let term = null;
let fitAddon = null;
let terminalDataDisposable = null;

// LiDAR visualization state
let lidarWebSocket = null;
let lidarCanvas = null;
let lidarCtx = null;
let lidarFrameCount = 0;
let currentLidarIP = null;
let currentLidarPort = null;

// Initialize the application
document.addEventListener('DOMContentLoaded', () => {
    loadGroups();
    setupUploadForm();
    initTerminal();
    initLidarCanvas();
});

// Switch between tabs
function switchTab(tabId) {
    // Hide all tab contents
    document.querySelectorAll('.tab-content').forEach(content => {
        content.classList.remove('active');
    });

    // Remove active state from all tab buttons
    document.querySelectorAll('.tab-btn').forEach(btn => {
        btn.classList.remove('active');
    });

    // Show the selected tab content
    const tabContent = document.getElementById(tabId);
    if (tabContent) {
        tabContent.classList.add('active');
    }

    // Add active state to the clicked button
    const buttonId = tabId === 'terminal-file-tab' ? 'tab-btn-terminal' : 'tab-btn-lidar';
    const button = document.getElementById(buttonId);
    if (button) {
        button.classList.add('active');
    }

    // Refit terminal when switching to terminal tab
    if (tabId === 'terminal-file-tab' && term && fitAddon) {
        setTimeout(() => {
            fitAddon.fit();
        }, 100);
    }
}

// Initialize empty terminal
function initTerminal() {
    const terminalContainer = document.getElementById('terminal-container');

    // Create xterm terminal
    term = new Terminal({
        cursorBlink: true,
        cursorStyle: 'block',
        fontSize: 12,
        fontFamily: 'Courier New, Consolas, monospace',
        theme: {
            background: '#0a0a0a',
            foreground: '#00ff88',
            cursor: '#00ff88',
            cursorAccent: '#0a0a0a',
            selection: 'rgba(0, 212, 255, 0.3)',
            black: '#0a0a0a',
            red: '#ff4757',
            green: '#00ff88',
            yellow: '#ffc107',
            blue: '#00d4ff',
            magenta: '#ff6b81',
            cyan: '#00d4ff',
            white: '#ffffff',
            brightBlack: '#555555',
            brightRed: '#ff6b81',
            brightGreen: '#00ff99',
            brightYellow: '#ffdd57',
            brightBlue: '#00e5ff',
            brightMagenta: '#ff8a9b',
            brightCyan: '#00e5ff',
            brightWhite: '#ffffff'
        },
        allowTransparency: true,
        scrollback: 5000
    });

    // Create fit addon
    fitAddon = new FitAddon.FitAddon();
    term.loadAddon(fitAddon);

    // Load web links addon
    const webLinksAddon = new WebLinksAddon.WebLinksAddon();
    term.loadAddon(webLinksAddon);

    // Open terminal in container
    term.open(terminalContainer);

    // Fit terminal to container
    setTimeout(() => {
        fitAddon.fit();
    }, 100);

    // Write initial message
    term.writeln('Select a node from the left panel to connect.');
    term.writeln('');

    // Handle terminal input - register only ONCE
    terminalDataDisposable = term.onData((data) => {
        if (terminalSocket && terminalSocket.readyState === WebSocket.OPEN) {
            terminalSocket.send(data);
        }
    });

    // Handle terminal resize
    window.addEventListener('resize', () => {
        if (fitAddon && term) {
            fitAddon.fit();
        }
    });
}

// Load node groups from the server
async function loadGroups() {
    try {
        const response = await fetch('/api/groups');
        groups = await response.json();
        renderGroups();
    } catch (error) {
        console.error('Failed to load groups:', error);
        alert('Failed to load node groups. Please refresh the page.');
    }
}

// Utility function to escape HTML
function escapeHtml(text) {
    const div = document.createElement('div');
    div.textContent = text;
    return div.innerHTML;
}

// Utility function to format file size
function formatSize(bytes) {
    if (bytes === 0) return '0 B';

    const k = 1024;
    const sizes = ['B', 'KB', 'MB', 'GB', 'TB'];
    const i = Math.floor(Math.log(bytes) / Math.log(k));

    return parseFloat((bytes / Math.pow(k, i)).toFixed(2)) + ' ' + sizes[i];
}

// Render the node groups in the sidebar
function renderGroups() {
    const container = document.getElementById('groups-container');
    container.innerHTML = '';

    groups.forEach((group, index) => {
        const groupDiv = document.createElement('div');
        groupDiv.className = 'group';

        groupDiv.innerHTML = `
            <div class="group-header" onclick="toggleGroup(${index})">
                <h3>${escapeHtml(group.name)}</h3>
                <span class="group-count">${group.nodes.length}</span>
            </div>
            <div class="group-nodes" id="group-${index}">
                ${group.nodes.map(node => `
                    <div class="node-item" id="node-${escapeHtml(node.nodeId)}" onclick="selectNode('${escapeHtml(node.nodeId)}')">
                        <div class="node-info">
                            <div class="node-id">Node #${escapeHtml(node.nodeId)}</div>
                            <div class="node-desc">${escapeHtml(node.description)}</div>
                            <div class="node-ip">${escapeHtml(node.IP)}</div>
                        </div>
                    </div>
                `).join('')}
            </div>
        `;

        container.appendChild(groupDiv);
    });
}

// Toggle group expansion
function toggleGroup(index) {
    const groupNodes = document.getElementById(`group-${index}`);
    groupNodes.classList.toggle('expanded');
}

// Find a node by ID
function findNode(nodeId) {
    for (const group of groups) {
        for (const node of group.nodes) {
            if (node.nodeId === nodeId) {
                return node;
            }
        }
    }
    return null;
}

// Select a node and connect to it
function selectNode(nodeId) {
    const node = findNode(nodeId);
    if (!node) {
        alert('Node not found');
        return;
    }

    // Update selected state in UI
    document.querySelectorAll('.node-item').forEach(item => {
        item.classList.remove('selected');
    });
    document.getElementById(`node-${nodeId}`).classList.add('selected');

    currentNode = node;

    // Update headers
    document.getElementById('terminal-node-name').textContent =
        `Node #${node.nodeId} - ${node.description}`;
    document.getElementById('file-node-name').textContent =
        `Node #${node.nodeId} - ${node.description}`;
    document.getElementById('lidar-node-name').textContent =
        `Node #${node.nodeId} - ${node.description}`;

    // Connect SSH terminal
    connectTerminal(node);

    // Load file manager
    document.getElementById('upload-node-id').value = nodeId;
    browsePath('~');

    // Reset LiDAR buttons
    resetLidarButtons();
}

// Connect SSH terminal using xterm.js
function connectTerminal(node) {
    // Close existing connection
    if (terminalSocket) {
        terminalSocket.close();
        terminalSocket = null;
    }

    // Clear terminal
    term.clear();
    term.writeln(`Connecting to ${node.IP}...`);

    // Connect to WebSocket
    const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
    const wsUrl = `${protocol}//${window.location.host}/api/ssh?nodeId=${node.nodeId}`;

    terminalSocket = new WebSocket(wsUrl);

    terminalSocket.onopen = () => {
        term.writeln('Connected!');
        term.writeln(`Host: ${node.IP}`);
        term.writeln(`User: ${node.Username}`);
        term.writeln('----------------------------------------');
        term.writeln('');
        fitAddon.fit();
    };

    terminalSocket.onmessage = (event) => {
        // Write data to terminal
        term.write(event.data);
    };

    terminalSocket.onerror = (error) => {
        term.writeln('');
        term.writeln('Error: Connection error');
    };

    terminalSocket.onclose = () => {
        term.writeln('');
        term.writeln('Connection closed.');
        term.writeln('Click on a node to reconnect.');
    };

    // Note: terminal input handler is registered only once in initTerminal()
}

// Browse to a specific path
async function browsePath(path) {
    if (!currentNode) {
        document.getElementById('file-list').innerHTML = '<div style="text-align: center; padding: 20px; color: #666;">Select a node first</div>';
        return;
    }

    const pathInput = document.getElementById('current-path');
    if (!path) {
        path = pathInput.value;
    }

    const fileList = document.getElementById('file-list');
    fileList.innerHTML = '<div class="loading"></div>';

    try {
        const response = await fetch(`/api/list?nodeId=${currentNode.nodeId}&path=${encodeURIComponent(path)}`);
        const data = await response.json();

        if (data.error) {
            fileList.innerHTML = `<div class="error">Error: ${escapeHtml(data.error)}</div>`;
            if (data.path) {
                pathInput.value = data.path;
            }
        } else {
            pathInput.value = data.path;
            document.getElementById('upload-path').value = data.path;
            renderFileList(data.files, data.path);
        }
    } catch (error) {
        fileList.innerHTML = `<div class="error">Error: ${escapeHtml(error.message)}</div>`;
    }
}

// Render file list
function renderFileList(files, currentPath) {
    const fileList = document.getElementById('file-list');
    fileList.innerHTML = '';

    if (files.length === 0) {
        fileList.innerHTML = '<div style="text-align: center; padding: 20px; color: #666;">Empty directory</div>';
        return;
    }

    // Sort: folders first, then files
    files.sort((a, b) => {
        if (a.isDir && !b.isDir) return -1;
        if (!a.isDir && b.isDir) return 1;
        return a.name.localeCompare(b.name);
    });

    files.forEach(file => {
        const fileItem = document.createElement('div');
        fileItem.className = 'file-item';

        const icon = file.isDir ? '📁' : '📄';
        const iconClass = file.isDir ? 'folder' : 'file';
        const size = file.isDir ? '-' : formatSize(file.size);

        fileItem.innerHTML = `
            <span class="file-icon ${iconClass}">${icon}</span>
            <span class="file-name">${escapeHtml(file.name)}</span>
            <span class="file-size">${size}</span>
            <span class="file-mtime">${file.mtime}</span>
            ${!file.isDir ? `<button class="btn btn-small btn-primary" onclick="downloadFile('${escapeHtml(currentPath)}/${escapeHtml(file.name)}')">⬇️</button>` : ''}
        `;

        if (file.isDir) {
            fileItem.addEventListener('click', () => {
                const newPath = currentPath === '~' ? `~/${file.name}` : `${currentPath}/${file.name}`;
                browsePath(newPath);
            });
        }

        fileList.appendChild(fileItem);
    });
}

// Go to parent directory
function goParent() {
    if (!currentNode) return;

    const pathInput = document.getElementById('current-path');
    const currentPath = pathInput.value;

    if (currentPath === '~' || currentPath === '/') {
        return;
    }

    const parts = currentPath.split('/');
    parts.pop();
    const parentPath = parts.join('/') || '~';

    browsePath(parentPath);
}

// Download file
function downloadFile(remotePath) {
    if (!currentNode) return;

    const url = `/api/download?nodeId=${currentNode.nodeId}&remotePath=${encodeURIComponent(remotePath)}`;

    // Create a temporary link and trigger download
    const a = document.createElement('a');
    a.href = url;
    a.download = remotePath.split('/').pop();
    document.body.appendChild(a);
    a.click();
    document.body.removeChild(a);
}

// Setup upload form
function setupUploadForm() {
    const form = document.getElementById('upload-form');

    form.addEventListener('submit', async (e) => {
        e.preventDefault();

        if (!currentNode) {
            const statusDiv = document.getElementById('upload-status');
            statusDiv.className = 'error';
            statusDiv.style.display = 'block';
            statusDiv.textContent = 'Select a node first';
            return;
        }

        const fileInput = document.getElementById('file-input');
        const pathInput = document.getElementById('upload-path');
        const statusDiv = document.getElementById('upload-status');

        if (!fileInput.files.length) {
            statusDiv.className = 'error';
            statusDiv.style.display = 'block';
            statusDiv.textContent = 'Please select a file to upload';
            return;
        }

        const formData = new FormData();
        formData.append('file', fileInput.files[0]);
        formData.append('nodeId', currentNode.nodeId);
        formData.append('remotePath', pathInput.value);

        statusDiv.className = '';
        statusDiv.style.display = 'block';
        statusDiv.textContent = 'Uploading...';

        try {
            const response = await fetch('/api/upload', {
                method: 'POST',
                body: formData
            });

            const data = await response.json();

            if (data.status === 'success') {
                statusDiv.className = 'success';
                statusDiv.textContent = data.message;
                fileInput.value = '';

                // Refresh file list
                browsePath(pathInput.value);
            } else {
                statusDiv.className = 'error';
                statusDiv.textContent = data.error || 'Upload failed';
            }
        } catch (error) {
            statusDiv.className = 'error';
            statusDiv.textContent = `Error: ${error.message}`;
        }
    });
}

// Check LiDAR connectivity
async function checkLidar(lidarIP, buttonNum, lidarPort) {
    if (!currentNode) {
        const statusDiv = document.getElementById('lidar-status');
        statusDiv.className = 'lidar-status error';
        statusDiv.style.display = 'block';
        statusDiv.textContent = 'Please select a node first';
        return;
    }

    const btn = document.getElementById(`lidar-btn-${buttonNum}`);
    const statusDiv = document.getElementById('lidar-status');

    // Reset button state to checking
    btn.classList.remove('lidar-btn-green', 'lidar-btn-gray');
    btn.classList.add('lidar-btn-checking');
    btn.disabled = true;

    statusDiv.className = 'lidar-status';
    statusDiv.style.display = 'block';
    statusDiv.textContent = `Checking connectivity to ${lidarIP}...`;

    try {
        const response = await fetch(`/api/check-lidar?nodeId=${currentNode.nodeId}&lidarIP=${lidarIP}`);
        const data = await response.json();

        btn.classList.remove('lidar-btn-checking');
        btn.disabled = false;

        if (data.success) {
            btn.classList.add('lidar-btn-green');
            statusDiv.className = 'lidar-status success';
            statusDiv.textContent = `✓ ${data.message}`;

            // Start LiDAR visualization when reachable
            startLidarVisualization(lidarIP, lidarPort);
        } else {
            btn.classList.add('lidar-btn-gray');
            statusDiv.className = 'lidar-status error';
            statusDiv.textContent = `✗ ${data.error}`;

            // Stop visualization if not reachable
            stopLidarVisualization();
        }
    } catch (error) {
        btn.classList.remove('lidar-btn-checking');
        btn.classList.add('lidar-btn-gray');
        btn.disabled = false;
        statusDiv.className = 'lidar-status error';
        statusDiv.textContent = `Error: ${error.message}`;

        // Stop visualization on error
        stopLidarVisualization();
    }
}

// Reset LiDAR buttons when changing nodes
function resetLidarButtons() {
    for (let i = 1; i <= 3; i++) {
        const btn = document.getElementById(`lidar-btn-${i}`);
        btn.classList.remove('lidar-btn-green', 'lidar-btn-checking');
        btn.classList.add('lidar-btn-gray');
        btn.disabled = false;
    }
    const statusDiv = document.getElementById('lidar-status');
    statusDiv.style.display = 'none';
    statusDiv.textContent = '';

    // Stop LiDAR visualization
    stopLidarVisualization();
}

// Initialize LiDAR canvas
function initLidarCanvas() {
    lidarCanvas = document.getElementById('lidar-canvas');
    if (lidarCanvas) {
        lidarCtx = lidarCanvas.getContext('2d');
        clearLidarCanvas();
    }
}

// Clear LiDAR canvas and draw grid
function clearLidarCanvas() {
    if (!lidarCtx || !lidarCanvas) return;

    const width = lidarCanvas.width;
    const height = lidarCanvas.height;

    // Clear canvas
    lidarCtx.fillStyle = '#0a0a0a';
    lidarCtx.fillRect(0, 0, width, height);

    // Draw grid
    lidarCtx.strokeStyle = '#333';
    lidarCtx.lineWidth = 1;

    // Range: X [-20, 20], Y [-10, 10]
    // Scale: 20 pixels per meter for X, 20 pixels per meter for Y
    const scaleX = width / 40; // 40 meters range
    const scaleY = height / 20; // 20 meters range

    // Draw vertical grid lines (X axis)
    for (let x = -20; x <= 20; x += 2) {
        const canvasX = (x + 20) * scaleX;
        lidarCtx.beginPath();
        lidarCtx.moveTo(canvasX, 0);
        lidarCtx.lineTo(canvasX, height);
        lidarCtx.stroke();

        // Draw X labels
        lidarCtx.fillStyle = '#666';
        lidarCtx.font = '10px Arial';
        lidarCtx.fillText(`${x}m`, canvasX - 10, height - 5);
    }

    // Draw horizontal grid lines (Y axis)
    for (let y = -10; y <= 10; y += 2) {
        const canvasY = (10 - y) * scaleY;
        lidarCtx.beginPath();
        lidarCtx.moveTo(0, canvasY);
        lidarCtx.lineTo(width, canvasY);
        lidarCtx.stroke();

        // Draw Y labels
        lidarCtx.fillStyle = '#666';
        lidarCtx.font = '10px Arial';
        lidarCtx.fillText(`${y}m`, 5, canvasY + 3);
    }

    // Draw center point (origin)
    lidarCtx.fillStyle = '#00d4ff';
    lidarCtx.beginPath();
    lidarCtx.arc(20 * scaleX, 10 * scaleY, 5, 0, 2 * Math.PI);
    lidarCtx.fill();

    // Draw axes
    lidarCtx.strokeStyle = '#00d4ff';
    lidarCtx.lineWidth = 2;

    // X axis (horizontal)
    lidarCtx.beginPath();
    lidarCtx.moveTo(0, 10 * scaleY);
    lidarCtx.lineTo(width, 10 * scaleY);
    lidarCtx.stroke();

    // Y axis (vertical)
    lidarCtx.beginPath();
    lidarCtx.moveTo(20 * scaleX, 0);
    lidarCtx.lineTo(20 * scaleX, height);
    lidarCtx.stroke();
}

// Render LiDAR points on canvas
function renderLidarPoints(points) {
    if (!lidarCtx || !lidarCanvas) return;

    clearLidarCanvas();

    const width = lidarCanvas.width;
    const height = lidarCanvas.height;

    // Scale: X [-20, 20] -> [0, width], Y [-10, 10] -> [height, 0]
    const scaleX = width / 40;
    const scaleY = height / 20;

    // Draw points
    for (const point of points) {
        // Convert coordinates to canvas coordinates
        const canvasX = (point.x + 20) * scaleX;
        const canvasY = (10 - point.y) * scaleY;

        // Color based on distance (R value)
        const distance = point.r;
        let color;
        if (distance < 5) {
            color = '#ff4757'; // Red for close objects
        } else if (distance < 10) {
            color = '#ffc107'; // Yellow for medium distance
        } else if (distance < 15) {
            color = '#00ff88'; // Green for farther objects
        } else {
            color = '#00d4ff'; // Cyan for very far objects
        }

        // Draw point
        lidarCtx.fillStyle = color;
        lidarCtx.beginPath();
        lidarCtx.arc(canvasX, canvasY, 2, 0, 2 * Math.PI);
        lidarCtx.fill();
    }

    // Update point count
    document.getElementById('lidar-point-count').textContent = `Points: ${points.length}`;
}

// Start LiDAR visualization
function startLidarVisualization(lidarIP, lidarPort) {
    if (!currentNode) {
        alert('Please select a node first');
        return;
    }

    // Stop existing visualization
    stopLidarVisualization();

    currentLidarIP = lidarIP;
    currentLidarPort = lidarPort;

    // Update title
    document.getElementById('lidar-visualization-title').textContent = `LiDAR ${lidarIP} (Port: ${lidarPort})`;

    // Show stop button
    document.getElementById('lidar-stop-btn').style.display = 'inline-block';

    // Reset frame count
    lidarFrameCount = 0;

    // Connect to WebSocket
    const protocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
    const wsUrl = `${protocol}//${window.location.host}/api/lidar-ws?nodeId=${currentNode.nodeId}&lidarPort=${lidarPort}`;

    lidarWebSocket = new WebSocket(wsUrl);

    lidarWebSocket.onopen = () => {
        console.log('LiDAR WebSocket connected');
        clearLidarCanvas();
    };

    lidarWebSocket.onmessage = (event) => {
        try {
            const data = JSON.parse(event.data);
            if (data.points && Array.isArray(data.points)) {
                renderLidarPoints(data.points);
                lidarFrameCount++;
                document.getElementById('lidar-frame-counter').textContent = `Frames: ${lidarFrameCount}`;
            }
        } catch (error) {
            console.error('Error parsing LiDAR data:', error);
        }
    };

    lidarWebSocket.onerror = (error) => {
        console.error('LiDAR WebSocket error:', error);
        document.getElementById('lidar-visualization-title').textContent = 'Connection Error';
    };

    lidarWebSocket.onclose = () => {
        console.log('LiDAR WebSocket closed');
        document.getElementById('lidar-stop-btn').style.display = 'none';
    };
}

// Stop LiDAR visualization
function stopLidarVisualization() {
    if (lidarWebSocket) {
        lidarWebSocket.close();
        lidarWebSocket = null;
    }

    currentLidarIP = null;
    currentLidarPort = null;

    // Update UI
    document.getElementById('lidar-visualization-title').textContent = 'No LiDAR selected';
    document.getElementById('lidar-stop-btn').style.display = 'none';
    document.getElementById('lidar-frame-counter').textContent = 'Frames: 0';
    document.getElementById('lidar-point-count').textContent = 'Points: 0';

    // Clear canvas
    clearLidarCanvas();
}