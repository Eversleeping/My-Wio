import { afterEach, expect, test, vi } from "vitest";
import { ImageCompressionError, compressImage } from "./imageCompression";

type WorkerRequest = { requestId: string; file: File };

class MockFileReader {
  result: string | ArrayBuffer | null = null;
  onload: (() => void) | null = null;
  onerror: (() => void) | null = null;

  readAsDataURL(blob: Blob) {
    this.result = `data:${blob.type};base64,encoded`;
    queueMicrotask(() => this.onload?.());
  }
}

function imageFile(type = "image/png") {
  return new File(["image"], "photo.png", { type });
}

function installURLMock() {
  const createObjectURL = vi.fn(() => "blob:test-image");
  const revokeObjectURL = vi.fn();
  class TestURL extends URL {}
  Object.defineProperties(TestURL, {
    createObjectURL: { value: createObjectURL },
    revokeObjectURL: { value: revokeObjectURL }
  });
  vi.stubGlobal("URL", TestURL);
  return { createObjectURL, revokeObjectURL };
}

function installMainThreadCanvas() {
  const originalCreateElement = document.createElement.bind(document);
  vi.spyOn(document, "createElement").mockImplementation(((tagName: string) => {
    if (tagName.toLowerCase() === "img") {
      const image = { onload: null as null | (() => void), onerror: null as null | (() => void), naturalWidth: 2400, naturalHeight: 1200 } as unknown as HTMLImageElement;
      Object.defineProperty(image, "src", { set: () => queueMicrotask(() => image.onload?.(new Event("load"))) });
      return image;
    }
    if (tagName.toLowerCase() === "canvas") {
      return {
        width: 0,
        height: 0,
        getContext: () => ({ drawImage: vi.fn() }),
        toBlob: (callback: (blob: Blob | null) => void) => callback(new Blob(["compressed"], { type: "image/webp" }))
      } as unknown as HTMLCanvasElement;
    }
    return originalCreateElement(tagName);
  }) as typeof document.createElement);
}

afterEach(() => {
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
});

test("uses the matching worker reply and terminates the worker after success", async () => {
  const instances: SuccessfulWorker[] = [];
  class SuccessfulWorker {
    onmessage: ((event: MessageEvent) => void) | null = null;
    onerror: ((event: ErrorEvent) => void) | null = null;
    onmessageerror: (() => void) | null = null;
    terminate = vi.fn();

    constructor() {
      instances.push(this);
    }

    postMessage(request: WorkerRequest) {
      queueMicrotask(() => this.onmessage?.({ data: { type: "success", requestId: "stale-request", blob: new Blob(["stale"], { type: "image/png" }) } } as MessageEvent));
      queueMicrotask(() => this.onmessage?.({ data: { type: "success", requestId: request.requestId, blob: new Blob(["ok"], { type: "image/webp" }) } } as MessageEvent));
    }
  }
  vi.stubGlobal("Worker", SuccessfulWorker);
  vi.stubGlobal("FileReader", MockFileReader);
  const url = installURLMock();

  await expect(compressImage(imageFile())).resolves.toBe("data:image/webp;base64,encoded");
  expect(instances).toHaveLength(1);
  expect(instances[0].terminate).toHaveBeenCalledOnce();
  expect(url.createObjectURL).not.toHaveBeenCalled();
});

test("falls back to the main thread when the worker reports a failure and releases resources", async () => {
  const instances: FailingWorker[] = [];
  class FailingWorker {
    onmessage: ((event: MessageEvent) => void) | null = null;
    onerror: ((event: ErrorEvent) => void) | null = null;
    onmessageerror: (() => void) | null = null;
    terminate = vi.fn();

    constructor() {
      instances.push(this);
    }

    postMessage(request: WorkerRequest) {
      queueMicrotask(() => this.onmessage?.({ data: { type: "error", requestId: request.requestId, code: "processing_failed", message: "OffscreenCanvas unavailable" } } as MessageEvent));
    }
  }
  vi.stubGlobal("Worker", FailingWorker);
  vi.stubGlobal("createImageBitmap", undefined);
  vi.stubGlobal("FileReader", MockFileReader);
  const url = installURLMock();
  installMainThreadCanvas();

  await expect(compressImage(imageFile("image/jpeg"))).resolves.toBe("data:image/webp;base64,encoded");
  expect(instances[0].terminate).toHaveBeenCalledOnce();
  expect(url.createObjectURL).toHaveBeenCalledOnce();
  expect(url.revokeObjectURL).toHaveBeenCalledWith("blob:test-image");
});

test("uses the main-thread fallback when Worker is unsupported", async () => {
  vi.stubGlobal("Worker", undefined);
  vi.stubGlobal("createImageBitmap", undefined);
  vi.stubGlobal("FileReader", MockFileReader);
  const url = installURLMock();
  installMainThreadCanvas();

  await expect(compressImage(imageFile("image/webp"))).resolves.toBe("data:image/webp;base64,encoded");
  expect(url.revokeObjectURL).toHaveBeenCalledOnce();
});

test("rejects unsupported file types before allocating a worker or object URL", async () => {
  const WorkerSpy = vi.fn();
  vi.stubGlobal("Worker", WorkerSpy);
  const url = installURLMock();

  await expect(compressImage(imageFile("image/gif"))).rejects.toMatchObject({ code: "unsupported_type" } satisfies Partial<ImageCompressionError>);
  expect(WorkerSpy).not.toHaveBeenCalled();
  expect(url.createObjectURL).not.toHaveBeenCalled();
});

test("does not repeat compression on the main thread after a terminal worker size result", async () => {
  class OversizedWorker {
    onmessage: ((event: MessageEvent) => void) | null = null;
    onerror: ((event: ErrorEvent) => void) | null = null;
    onmessageerror: (() => void) | null = null;
    terminate = vi.fn();
    postMessage(request: WorkerRequest) {
      queueMicrotask(() => this.onmessage?.({ data: { type: "error", requestId: request.requestId, code: "image_too_large", message: "Still too large" } } as MessageEvent));
    }
  }
  vi.stubGlobal("Worker", OversizedWorker);
  const createElement = vi.spyOn(document, "createElement");

  await expect(compressImage(imageFile())).rejects.toMatchObject({ code: "image_too_large" } satisfies Partial<ImageCompressionError>);
  expect(createElement).not.toHaveBeenCalled();
});

test("terminates an in-flight worker when compression is cancelled", async () => {
  const instances: SilentWorker[] = [];
  class SilentWorker {
    onmessage: ((event: MessageEvent) => void) | null = null;
    onerror: ((event: ErrorEvent) => void) | null = null;
    onmessageerror: (() => void) | null = null;
    terminate = vi.fn();
    constructor() { instances.push(this); }
    postMessage() {}
  }
  vi.stubGlobal("Worker", SilentWorker);
  const controller = new AbortController();
  const compression = compressImage(imageFile(), { signal: controller.signal });
  controller.abort();

  await expect(compression).rejects.toMatchObject({ code: "cancelled" } satisfies Partial<ImageCompressionError>);
  expect(instances[0].terminate).toHaveBeenCalledOnce();
});
