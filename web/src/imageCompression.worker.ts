const SUPPORTED_IMAGE_TYPES = new Set(["image/png", "image/jpeg", "image/webp"]);
const MAX_DIMENSION = 1600;
const TARGET_SIZE_BYTES = 900 * 1024;
const MAX_ATTEMPTS = 6;

type CompressionRequest = {
  type: "compress";
  requestId: string;
  file: File;
};

type CompressionResponse =
  | { type: "success"; requestId: string; blob: Blob }
  | { type: "error"; requestId: string; code: string; message: string };

const workerScope = self as unknown as {
  addEventListener: (type: "message", listener: (event: MessageEvent<CompressionRequest>) => void) => void;
  postMessage: (message: CompressionResponse) => void;
};

workerScope.addEventListener("message", event => {
  const request = event.data;
  if (!request || request.type !== "compress" || !request.requestId || !request.file) return;
  void compress(request).then(
    blob => workerScope.postMessage({ type: "success", requestId: request.requestId, blob }),
    error => workerScope.postMessage({ type: "error", requestId: request.requestId, ...toWorkerError(error) })
  );
});

async function compress(request: CompressionRequest): Promise<Blob> {
  if (!SUPPORTED_IMAGE_TYPES.has(request.file.type.toLowerCase())) throw new Error(`Unsupported image type: ${request.file.type || "(empty)"}`);
  if (typeof createImageBitmap !== "function" || typeof OffscreenCanvas === "undefined") {
    throw new Error("Worker image APIs (createImageBitmap and OffscreenCanvas) are unavailable");
  }

  const bitmap = await createImageBitmap(request.file);
  try {
    if (bitmap.width < 1 || bitmap.height < 1) throw new Error("Image has no readable dimensions");
    let maxDimension = MAX_DIMENSION;
    let quality = 0.84;
    for (let attempt = 0; attempt < MAX_ATTEMPTS; attempt++) {
      const scale = Math.min(1, maxDimension / Math.max(bitmap.width, bitmap.height));
      const canvas = new OffscreenCanvas(Math.max(1, Math.round(bitmap.width * scale)), Math.max(1, Math.round(bitmap.height * scale)));
      const context = canvas.getContext("2d");
      if (!context) throw new Error("Could not create a 2D canvas context");
      context.drawImage(bitmap, 0, 0, canvas.width, canvas.height);
      const blob = await canvas.convertToBlob({ type: "image/webp", quality });
      if (blob.size <= TARGET_SIZE_BYTES) return blob;

      maxDimension = Math.round(maxDimension * 0.82);
      quality = Math.max(0.62, quality - 0.05);
      await yieldToEventLoop();
    }
    throw new Error(`Image remains over ${TARGET_SIZE_BYTES} bytes after ${MAX_ATTEMPTS} attempts`);
  } finally {
    bitmap.close();
  }
}

function toWorkerError(error: unknown) {
  const message = error instanceof Error ? error.message : String(error);
  return {
    code: message.startsWith("Image remains over") ? "image_too_large" : "processing_failed",
    message
  };
}

function yieldToEventLoop() {
  return new Promise<void>(resolve => setTimeout(resolve, 0));
}
