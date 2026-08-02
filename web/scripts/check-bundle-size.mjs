import { existsSync, readdirSync, readFileSync, statSync } from "node:fs";
import { join, relative, resolve } from "node:path";
import { gzipSync } from "node:zlib";

const MAX_JAVASCRIPT_CHUNK_BYTES = 500_000;
const distDirectory = resolve(process.cwd(), "dist");
const indexPath = join(distDirectory, "index.html");

function collectJavaScriptFiles(directory) {
  return readdirSync(directory, { withFileTypes: true }).flatMap((entry) => {
    const path = join(directory, entry.name);
    if (entry.isDirectory()) {
      return collectJavaScriptFiles(path);
    }

    return entry.isFile() && entry.name.endsWith(".js") ? [path] : [];
  });
}

function formatKilobytes(bytes) {
  return `${(bytes / 1000).toFixed(2)} kB`;
}

if (!existsSync(distDirectory)) {
  console.error("Bundle-size check requires an existing production build in ./dist.");
  process.exitCode = 1;
} else {
  const chunks = collectJavaScriptFiles(distDirectory)
    .map((path) => {
      const source = readFileSync(path);
      return {
        file: relative(process.cwd(), path).replaceAll("\\", "/"),
        rawBytes: statSync(path).size,
        gzipBytes: gzipSync(source, { level: 9 }).length
      };
    })
    .sort((left, right) => right.rawBytes - left.rawBytes || left.file.localeCompare(right.file));

  if (chunks.length === 0) {
    console.error("Bundle-size check did not find JavaScript output in ./dist.");
    process.exitCode = 1;
  } else {
    console.log(`JavaScript chunk limit: ${formatKilobytes(MAX_JAVASCRIPT_CHUNK_BYTES)} raw`);
    for (const chunk of chunks) {
      console.log(`${chunk.file}: ${formatKilobytes(chunk.rawBytes)} raw, ${formatKilobytes(chunk.gzipBytes)} gzip`);
    }

    const oversizedChunks = chunks.filter((chunk) => chunk.rawBytes > MAX_JAVASCRIPT_CHUNK_BYTES);
    if (oversizedChunks.length > 0) {
      console.error("Oversized JavaScript chunks:");
      for (const chunk of oversizedChunks) {
        console.error(`- ${chunk.file} (${formatKilobytes(chunk.rawBytes)} raw)`);
      }
      process.exitCode = 1;
    }

    const indexHTML = existsSync(indexPath) ? readFileSync(indexPath, "utf8") : "";
    if (indexHTML.includes("vendor-markdown")) {
      console.error("Codex-only Markdown dependencies must not be preloaded by the application entry point.");
      process.exitCode = 1;
    }
  }
}
