import { api, ApiError } from "../../types/api";

export interface ChatFileInfo {
  id: string;
  name: string;
  media_type: string;
  kind: "image" | "pdf" | "text";
  size: number;
}

export function uploadChatFile(
  chatId: string,
  file: File,
  onProgress: (fraction: number) => void,
  signal: AbortSignal,
): Promise<ChatFileInfo> {
  return new Promise((resolve, reject) => {
    const cancelled = () => new DOMException("Upload cancelled", "AbortError");
    if (signal.aborted) return reject(cancelled());
    const xhr = new XMLHttpRequest();
    xhr.open("POST", `/api/v1/chats/${encodeURIComponent(chatId)}/files`);
    xhr.withCredentials = true;
    xhr.upload.onprogress = (e) => {
      if (e.lengthComputable) onProgress(e.loaded / e.total);
    };
    xhr.onload = () => {
      let data: unknown = null;
      try {
        data = JSON.parse(xhr.responseText);
      } catch {
        /* body was not JSON */
      }
      if (xhr.status >= 200 && xhr.status < 300 && data)
        return resolve(data as ChatFileInfo);
      if (xhr.status === 401)
        window.dispatchEvent(new Event("factory:unauthorized"));
      const err = (data as { error?: { code?: string; message?: string } })
        ?.error;
      reject(
        new ApiError(
          xhr.status,
          err?.code ?? "HTTP_ERROR",
          err?.message?.replace(/^invalid request: /i, "") ??
            (xhr.status === 413
              ? "File is too large."
              : `Upload failed (${xhr.status}).`),
        ),
      );
    };
    xhr.onerror = () =>
      reject(new Error("Upload failed. Check your connection."));
    xhr.onabort = () => reject(cancelled());
    signal.addEventListener("abort", () => xhr.abort(), { once: true });
    const form = new FormData();
    form.append("file", file, file.name);
    xhr.send(form);
  });
}

export const deleteChatFile = (chatId: string, fileId: string) =>
  api.del(
    `/api/v1/chats/${encodeURIComponent(chatId)}/files/${encodeURIComponent(fileId)}`,
  );
