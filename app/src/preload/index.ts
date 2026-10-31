import { contextBridge, ipcRenderer } from "electron";

// What the page may ask the main process for.
const api = {
  daemon: (): Promise<{ addr: string; token: string }> => ipcRenderer.invoke("daemon"),
  pickFolder: (): Promise<string | null> => ipcRenderer.invoke("pick-folder"),
  // reveal opens a folder in the file manager; it returns an error
  // message, or "" on success.
  reveal: (p: string): Promise<string> => ipcRenderer.invoke("reveal", p),
};

export type RlaApi = typeof api;

contextBridge.exposeInMainWorld("rla", api);
