// The app's main process: it finds the rla daemon, or starts one, and
// opens the window. The daemon owns the agents; closing the window leaves
// them running.
import { app, BrowserWindow, dialog, ipcMain, shell } from "electron";
import { spawn } from "node:child_process";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";

type Daemon = { addr: string; token: string; version?: string };

const home = process.env.RLA_HOME || path.join(os.homedir(), ".rla");

// rlaBin is the daemon binary: $RLA_BIN, the one shipped beside the app,
// or in development the repo's bin/rla.
function rlaBin(): string {
  if (process.env.RLA_BIN) return process.env.RLA_BIN;
  if (app.isPackaged) return path.join(process.resourcesPath, "rla");
  return path.resolve(__dirname, "../../../bin/rla");
}

function readDaemon(): Daemon | null {
  try {
    return JSON.parse(fs.readFileSync(path.join(home, "daemon.json"), "utf8"));
  } catch {
    return null;
  }
}

async function ping(d: Daemon): Promise<boolean> {
  try {
    const res = await fetch(`http://${d.addr}/api/ping`, {
      headers: { Authorization: `Bearer ${d.token}` },
      signal: AbortSignal.timeout(1500),
    });
    return res.ok;
  } catch {
    return false;
  }
}

// startDaemon runs `rla daemon` in a login shell, so agents get the PATH
// and environment of the user's terminal, not the bare one apps get.
function startDaemon(): void {
  fs.mkdirSync(home, { recursive: true, mode: 0o700 });
  const log = fs.openSync(path.join(home, "daemon.log"), "a");
  const shellPath = process.env.SHELL || "/bin/zsh";
  const child = spawn(shellPath, ["-l", "-c", 'exec "$0" daemon', rlaBin()], {
    detached: true,
    stdio: ["ignore", log, log],
    env: { ...process.env, RLA_HOME: home },
  });
  child.unref();
}

let starting: Promise<Daemon> | null = null;

// ensureDaemon returns a daemon that answers, starting one if needed.
function ensureDaemon(): Promise<Daemon> {
  starting ??= (async () => {
    const found = readDaemon();
    if (found && (await ping(found))) return found;

    startDaemon();
    for (let i = 0; i < 100; i++) {
      await new Promise((r) => setTimeout(r, 100));
      const d = readDaemon();
      if (d && (await ping(d))) return d;
    }
    throw new Error(`rla daemon did not start; see ${path.join(home, "daemon.log")}`);
  })().finally(() => {
    starting = null;
  });
  return starting;
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
    },
  });

  // Links open in the browser, never in the app.
  win.webContents.setWindowOpenHandler(({ url }) => {
    if (/^https?:/.test(url)) void shell.openExternal(url);
    return { action: "deny" };
  });

  if (process.env.ELECTRON_RENDERER_URL) {
    void win.loadURL(process.env.ELECTRON_RENDERER_URL);
  } else {
    void win.loadFile(path.join(__dirname, "../renderer/index.html"));
  }
}

ipcMain.handle("daemon", () => ensureDaemon());

ipcMain.handle("pick-folder", async (e) => {
  const win = BrowserWindow.fromWebContents(e.sender);
  const opts: Electron.OpenDialogOptions = { properties: ["openDirectory", "createDirectory"] };
  const res = win ? await dialog.showOpenDialog(win, opts) : await dialog.showOpenDialog(opts);
  return res.canceled ? null : res.filePaths[0];
});

ipcMain.handle("reveal", (_e, p: string) => shell.openPath(p));

void app.whenReady().then(() => {
  createWindow();
  app.on("activate", () => {
    if (BrowserWindow.getAllWindows().length === 0) createWindow();
  });
});

// The agents live in the daemon; quitting the app does not stop them.
app.on("window-all-closed", () => {
  if (process.platform !== "darwin") app.quit();
});
