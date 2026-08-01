const SUPPORTED_IMAGE_TYPES = new Set(["image/png", "image/jpeg", "image/webp"]);
const MAX_DIMENSION = 1600;
const TARGET_SIZE_BYTES = 900 * 1024;
const MAX_ATTEMPTS = 6;
const WORKER_TIMEOUT_MS = 15_000;

type WorkerRequest = {
  type: "compress";
  requestId: string;
  file: File;
};

type WorkerResponse = {
  type: "success" | "error";
  requestId: string;
  blob?: Blob;
  code?: string;
  message?: string;
};

type DecodedImage = {
  source: CanvasImageSource;
  width: number;
  height: number;
  dispose: () => void;
};

export type ImageCompressionErrorCode = "unsupported_type" | "unavailable" | "decode_failed" | "processing_failed" | "image_too_large" | "worker_failed" | "compression_failed" | "cancelled";

export type ImageCompressionOptions = {
  signal?: AbortSignal;
};

/** An error with a stable code that can be logged or surfaced by callers. */
export class ImageCompressionError extends Error {
  readonly code: ImageCompressionErrorCode;
  readonly cause?: unknown;

  constructor(code: ImageCompressionErrorCode, message: string, cause?: unknown) {
    super(`${code}: ${message}`);
    this.name = "ImageCompressionError";
    this.code = code;
    this.cause = cause;
  }
}

/**
 * Converts a supported image into a WebP data URL suitable for the existing
 * composer API.  It prefers a module worker, but always retries on the main
 * thread when worker creation, communication, decoding, or compression fails.
 */
export async function compressImage(file: File, options: ImageCompressionOptions = {}): Promise<string> {
  assertSupportedImage(file);
  throwIfAborted(options.signal);

  let workerFailure: unknown;
  if (typeof Worker !== "undefined") {
    try {
      return await blobToDataURL(await compressInWorker(file, options.signal));
    } catch (error) {
      if (options.signal?.aborted || isTerminalWorkerResult(error)) throw error;
      workerFailure = error;
    }
  }

  try {
    return await compressOnMainThread(file, options.signal);
  } catch (fallbackFailure) {
    if (options.signal?.aborted) throw fallbackFailure;
    const message = workerFailure
      ? `Worker path failed (${describeError(workerFailure)}); main-thread fallback failed (${describeError(fallbackFailure)})`
      : `Main-thread fallback failed (${describeError(fallbackFailure)})`;
    throw new ImageCompressionError("compression_failed", message, fallbackFailure);
  }
}

function assertSupportedImage(file: File) {
  if (!SUPPORTED_IMAGE_TYPES.has(file.type.toLowerCase())) {
    throw new ImageCompressionError("unsupported_type", `Unsupported image type: ${file.type || "(empty)"}`);
  }
}

function compressInWorker(file: File, signal?: AbortSignal): Promise<Blob> {
  throwIfAborted(signal);
  let worker: Worker;
  try {
    worker = new Worker(new URL("./imageCompression.worker.ts", import.meta.url), { type: "module" });
  } catch (error) {
    return Promise.reject(new ImageCompressionError("worker_failed", `Could not start image worker: ${describeError(error)}`, error));
  }

  const requestId = nextRequestId();
  return new Promise<Blob>((resolve, reject) => {
    let settled = false;
    const finish = (callback: (value: Blob | ImageCompressionError) => void, value: Blob | ImageCompressionError) => {
      if (settled) return;
      settled = true;
      clearTimeout(timeout);
      worker.onmessage = null;
      worker.onerror = null;
      worker.onmessageerror = null;
      signal?.removeEventListener("abort", abort);
      worker.terminate();
      callback(value);
    };
    const fail = (message: string, cause?: unknown) => finish(reject as (value: Blob | ImageCompressionError) => void, new ImageCompressionError("worker_failed", message, cause));
    const abort = () => finish(reject as (value: Blob | ImageCompressionError) => void, cancelledError());
    const timeout = setTimeout(() => fail(`Image worker timed out after ${WORKER_TIMEOUT_MS}ms`), WORKER_TIMEOUT_MS);

    worker.onmessage = event => {
      const response = event.data as WorkerResponse;
      // A worker may emit stale messages while it is being torn down.  Only
      // resolve the request that originated this promise.
      if (!response || response.requestId !== requestId) return;
      if (response.type === "success" && response.blob instanceof Blob) {
        finish(resolve as (value: Blob | ImageCompressionError) => void, response.blob);
        return;
      }
      if (response.type === "error" && response.code === "image_too_large") {
        finish(reject as (value: Blob | ImageCompressionError) => void, new ImageCompressionError("image_too_large", response.message || "Image remains over the compressed size limit"));
        return;
      }
      fail(response.message || `Image worker returned ${response.code || "an invalid response"}`);
    };
    worker.onerror = event => fail(event.message || "Image worker raised an error", event.error);
    worker.onmessageerror = () => fail("Image worker returned an unreadable message");

    signal?.addEventListener("abort", abort, { once: true });
    if (signal?.aborted) {
      abort();
      return;
    }
    try {
      worker.postMessage({ type: "compress", requestId, file } satisfies WorkerRequest);
    } catch (error) {
      fail(`Could not send image to worker: ${describeError(error)}`, error);
    }
  });
}

async function compressOnMainThread(file: File, signal?: AbortSignal): Promise<string> {
  const image = await decodeOnMainThread(file, signal);
  try {
    const blob = await compressDecodedImage(image, signal);
    throwIfAborted(signal);
    return await blobToDataURL(blob);
  } finally {
    image.dispose();
  }
}

async function decodeOnMainThread(file: File, signal?: AbortSignal): Promise<DecodedImage> {
  throwIfAborted(signal);
  if (typeof createImageBitmap === "function") {
    try {
      const bitmap = await createImageBitmap(file);
      if (signal?.aborted) {
        bitmap.close();
        throw cancelledError();
      }
      if (bitmap.width > 0 && bitmap.height > 0) {
        return { source: bitmap, width: bitmap.width, height: bitmap.height, dispose: () => bitmap.close() };
      }
      bitmap.close();
    } catch {
      throwIfAborted(signal);
      // createImageBitmap is not consistently implemented for all codecs. A
      // regular image element remains the compatibility path, including jsdom.
    }
  }

  if (typeof URL.createObjectURL !== "function") {
    throw new ImageCompressionError("unavailable", "Neither createImageBitmap nor URL.createObjectURL is available");
  }
  const sourceURL = URL.createObjectURL(file);
  const image = document.createElement("img");
  image.decoding = "async";
  try {
    await new Promise<void>((resolve, reject) => {
      const cleanup = () => signal?.removeEventListener("abort", abort);
      const abort = () => { cleanup(); image.src = ""; reject(cancelledError()); };
      image.onload = () => { cleanup(); resolve(); };
      image.onerror = () => { cleanup(); reject(new ImageCompressionError("decode_failed", "Could not read image")); };
      signal?.addEventListener("abort", abort, { once: true });
      if (signal?.aborted) { abort(); return; }
      image.src = sourceURL;
    });
    if (image.naturalWidth < 1 || image.naturalHeight < 1) {
      throw new ImageCompressionError("decode_failed", "Image has no readable dimensions");
    }
    return {
      source: image,
      width: image.naturalWidth,
      height: image.naturalHeight,
      dispose: () => URL.revokeObjectURL(sourceURL)
    };
  } catch (error) {
    URL.revokeObjectURL(sourceURL);
    throw error;
  }
}

async function compressDecodedImage(image: DecodedImage, signal?: AbortSignal): Promise<Blob> {
  let maxDimension = MAX_DIMENSION;
  let quality = 0.84;
  for (let attempt = 0; attempt < MAX_ATTEMPTS; attempt++) {
    throwIfAborted(signal);
    const scale = Math.min(1, maxDimension / Math.max(image.width, image.height));
    const canvas = document.createElement("canvas");
    canvas.width = Math.max(1, Math.round(image.width * scale));
    canvas.height = Math.max(1, Math.round(image.height * scale));
    const context = canvas.getContext("2d");
    if (!context) throw new ImageCompressionError("processing_failed", "Could not create a 2D canvas context");
    context.drawImage(image.source, 0, 0, canvas.width, canvas.height);
    const blob = await canvasToBlob(canvas, quality);
    throwIfAborted(signal);
    if (blob.size <= TARGET_SIZE_BYTES) return blob;

    maxDimension = Math.round(maxDimension * 0.82);
    quality = Math.max(0.62, quality - 0.05);
    // toBlob is asynchronous, but yielding here also gives rendering and input
    // a turn before allocating the next canvas during a large-image retry.
    await yieldToEventLoop();
  }
  throw new ImageCompressionError("image_too_large", `Image remains over ${TARGET_SIZE_BYTES} bytes after ${MAX_ATTEMPTS} attempts`);
}

function canvasToBlob(canvas: HTMLCanvasElement, quality: number): Promise<Blob> {
  return new Promise((resolve, reject) => {
    try {
      canvas.toBlob(blob => {
        if (blob) resolve(blob);
        else reject(new ImageCompressionError("processing_failed", "Canvas could not encode WebP"));
      }, "image/webp", quality);
    } catch (error) {
      reject(new ImageCompressionError("processing_failed", `Canvas encoding failed: ${describeError(error)}`, error));
    }
  });
}

function blobToDataURL(blob: Blob): Promise<string> {
  return new Promise((resolve, reject) => {
    const reader = new FileReader();
    reader.onload = () => resolve(String(reader.result));
    reader.onerror = () => reject(new ImageCompressionError("processing_failed", "Could not read compressed image"));
    reader.readAsDataURL(blob);
  });
}

function yieldToEventLoop() {
  return new Promise<void>(resolve => setTimeout(resolve, 0));
}

let requestSequence = 0;
function nextRequestId() {
  requestSequence += 1;
  return globalThis.crypto?.randomUUID?.() || `image-compression-${Date.now()}-${requestSequence}`;
}

function describeError(error: unknown) {
  return error instanceof Error ? error.message : String(error);
}

function isTerminalWorkerResult(error: unknown) {
  return error instanceof ImageCompressionError && error.code === "image_too_large";
}

function cancelledError() {
  return new ImageCompressionError("cancelled", "Image compression was cancelled");
}

function throwIfAborted(signal?: AbortSignal) {
  if (signal?.aborted) throw cancelledError();
}
