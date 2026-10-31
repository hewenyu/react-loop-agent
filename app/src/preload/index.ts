import { contextBridge, ipcRenderer } from "electron";

// What the page may ask the main process for.
contextBridge.exposeInMainWorld("rla", {
  daemon: (): Promise<{ addr: string; token: string }> => ipcRenderer.invoke("daemon"),
  pickFolder: (): Promise<string | null> => ipcRenderer.invoke("pick-folder"),
  reveal: (p: string): Promise<string> => ipcRenderer.invoke("reveal", p),
});
