// The app's main process: it finds the rla daemon, or starts one, and
// opens the window. The daemon owns the agents; closing the window leaves
// them running.
import { app, BrowserWindow, dialog, ipcMain, session, shell } from "electron";
import type { IpcMainInvokeEvent } from "electron";
import { spawn } from "node:child_process";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import { pathToFileURL } from "node:url";

// What the page gets to reach the daemon.
type Daemon = { addr: string; token: string };

const home = process.env.RLA_HOME || path.join(os.homedir(), ".rla");

// Waiting for a daemon: each ping may take PING_TIMEOUT_MS; a daemon we
// started has START_TIMEOUT_MS to answer.
const PING_TIMEOUT_MS = 1500;
const START_POLL_MS = 100;
const START_TIMEOUT_MS = 10_000;

// Permissions the page may have: notifications, and xterm copying to the
// clipboard. Everything else (camera, geolocation, …) is denied.
const ALLOWED_PERMISSIONS = new Set(["notifications", "clipboard-sanitized-write"]);

// The page: the dev server, only when running unpackaged, or the built file.
const devURL = !app.isPackaged ? process.env.ELECTRON_RENDERER_URL : undefined;
const pageFile = path.join(__dirname, "../renderer/index.html");

// rlaBin is the daemon binary: $RLA_BIN, the one shipped beside the app,
// or in development the repo's bin/rla.
function rlaBin(): string {
  if (process.env.RLA_BIN) return process.env.RLA_BIN;
  if (app.isPackaged) return path.join(process.resourcesPath, "rla");
  return path.resolve(__dirname, "../../../bin/rla");
}

function readDaemon(): Daemon | null {
  try {
    const d: unknown = JSON.parse(fs.readFileSync(path.join(home, "daemon.json"), "utf8"));
    if (typeof d !== "object" || d === null) return null;
    const { addr, token } = d as Record<string, unknown>;
    if (typeof addr !== "string" || typeof token !== "string" || !addr || !token) return null;
    return { addr, token };
  } catch {
    return null;
  }
}

async function ping(d: Daemon): Promise<boolean> {
  try {
    const res = await fetch(`http://${d.addr}/api/ping`, {
      headers: { Authorization: `Bearer ${d.token}` },
      signal: AbortSignal.timeout(PING_TIMEOUT_MS),
    });
    return res.ok;
  } catch {
    return false;
  }
}

// startDaemon runs `rla daemon` in a login shell, so agents get the PATH
// and environment of the user's terminal, not the bare one apps get. The
// binary is passed as $0, never spliced into the command string.
function startDaemon(): void {
  const bin = rlaBin();
  if (path.isAbsolute(bin) && !fs.existsSync(bin)) throw new Error(`rla binary not found at ${bin}; run \`make build\` or set RLA_BIN`);

  fs.mkdirSync(home, { recursive: true, mode: 0o700 });
  const log = fs.openSync(path.join(home, "daemon.log"), "a");
  try {
    const shellPath = process.env.SHELL || "/bin/zsh";
    const child = spawn(shellPath, ["-l", "-c", 'exec "$0" daemon', bin], {
      detached: true,
      stdio: ["ignore", log, log],
      env: { ...process.env, RLA_HOME: home },
    });
    // A shell that cannot start must not take the app down; the wait in
    // ensureDaemon reports it.
    child.on("error", (err) => console.error("starting rla daemon:", err));
    child.unref();
  } finally {
    // The child has its own copy.
    fs.closeSync(log);
  }
}

let starting: Promise<Daemon> | null = null;

// ensureDaemon returns a daemon that answers, starting one if needed.
function ensureDaemon(): Promise<Daemon> {
  starting ??= (async () => {
    const found = readDaemon();
    if (found && (await ping(found))) return found;

    startDaemon();
    const deadline = Date.now() + START_TIMEOUT_MS;
    while (Date.now() < deadline) {
      await new Promise((r) => setTimeout(r, START_POLL_MS));
      const d = readDaemon();
      if (d && (await ping(d))) return d;
    }
    throw new Error(`rla daemon did not start; see ${path.join(home, "daemon.log")}`);
  })().finally(() => {
    starting = null;
  });
  return starting;
}

// isAppURL tells the app's own pages from anything else.
function isAppURL(url: string): boolean {
  try {
    const u = new URL(url);
    if (devURL) return u.origin === new URL(devURL).origin;
    return u.protocol === "file:" && u.pathname === pathToFileURL(pageFile).pathname;
  } catch {
    return false;
  }
}

// trusted checks that an IPC call comes from the app's own page, not a
// frame it was navigated or tricked into.
function trusted(e: IpcMainInvokeEvent): boolean {
  const url = e.senderFrame?.url;
  return !!url && isAppURL(url);
}

function openExternal(url: string): void {
  try {
    const { protocol } = new URL(url);
    if (protocol === "https:" || protocol === "http:") void shell.openExternal(url);
  } catch {
    // Not a URL: ignore.
  }
}

function createWindow(): void {
  const win = new BrowserWindow({
    width: 1440,
    height: 900,
    minWidth: 960,
    minHeight: 600,
    title: "RLA 调度台",
    titleBarStyle: process.platform === "darwin" ? "hiddenInset" : "default",
    backgroundColor: "#171614",
    webPreferences: {
      preload: path.join(__dirname, "../preload/index.js"),
      contextIsolation: true,
      sandbox: true,
      nodeIntegration: false,
      webSecurity: true,
      webviewTag: false,
    },
  });

  // Links open in the browser, never in the app.
  win.webContents.setWindowOpenHandler(({ url }) => {
    openExternal(url);
    return { action: "deny" };
  });
  win.webContents.on("will-navigate", (e, url) => {
    if (isAppURL(url)) return;
    e.preventDefault();
    openExternal(url);
  });

  if (devURL) {
    void win.loadURL(devURL);
  } else {
    void win.loadFile(pageFile);
  }
}

ipcMain.handle("daemon", (e) => {
  if (!trusted(e)) throw new Error("untrusted sender");
  return ensureDaemon();
});

ipcMain.handle("pick-folder", async (e) => {
  if (!trusted(e)) throw new Error("untrusted sender");
  const win = BrowserWindow.fromWebContents(e.sender);
  const opts: Electron.OpenDialogOptions = { properties: ["openDirectory", "createDirectory"] };
  const res = win ? await dialog.showOpenDialog(win, opts) : await dialog.showOpenDialog(opts);
  return res.canceled ? null : (res.filePaths[0] ?? null);
});

// reveal opens a folder, never a file: opening a file could run it.
ipcMain.handle("reveal", async (e, p: unknown) => {
  if (!trusted(e)) throw new Error("untrusted sender");
  if (typeof p !== "string" || !path.isAbsolute(p)) return "not an absolute path";
  try {
    if (!(await fs.promises.stat(p)).isDirectory()) return `${p} is not a folder`;
  } catch {
    return `${p} does not exist`;
  }
  return shell.openPath(p);
});

void app.whenReady().then(() => {
  session.defaultSession.setPermissionRequestHandler((_wc, permission, callback) => {
    callback(ALLOWED_PERMISSIONS.has(permission));
  });

  createWindow();
  app.on("activate", () => {
    if (BrowserWindow.getAllWindows().length === 0) createWindow();
  });
});

// The agents live in the daemon; quitting the app does not stop them.
app.on("window-all-closed", () => {
  if (process.platform !== "darwin") app.quit();
});
