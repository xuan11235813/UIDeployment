// Global state
let groups = [];
let currentNode = null;
let terminalSocket = null;
let term = null;
let fitAddon = null;
let terminalDataDisposable = null;

// Initialize the application
document.addEventListener('DOMContentLoaded', () => {
    loadGroups();
    setupUploadForm();
    initTerminal();
});

// Initialize empty terminal
function initTerminal() {
    const terminalContainer = document.getElementById('terminal-container');

    // Create xterm terminal
    term = new Terminal({
        cursorBlink: true,
        cursorStyle: 'block',
        fontSize: 14,
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

    // Connect SSH terminal
    connectTerminal(node);

    // Load file manager
    document.getElementById('upload-node-id').value = nodeId;
    browsePath('~');
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