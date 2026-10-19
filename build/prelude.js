// The prelude implements the protocol of package runner on top of an applet.
// The applet's default export is called with an iterable of input objects and
// the params, and returns an iterable of output objects, usually a generator:
//
//   export default function* (input, params) {
//     for (const obj of input) yield obj;
//   }
//
// esbuild bundles this file with the applet into a function scope, so none of
// the declarations here are visible to the applet.
import applet from "applet";

const io = Javy.IO;
const encoder = new TextEncoder();
const decoder = new TextDecoder();
const chunk = new Uint8Array(64 << 10);
let pending = new Uint8Array(0);
let eof = false;

// current is the index of the input object being processed, or null.
let current = null;

function write(fd, text) {
  io.writeSync(fd, encoder.encode(text + "\n"));
}

function readLine() {
  for (;;) {
    const i = pending.indexOf(10);
    if (i >= 0) {
      const line = decoder.decode(pending.subarray(0, i));
      pending = pending.subarray(i + 1);
      return line;
    }
    if (eof) {
      return null;
    }
    const n = io.readSync(0, chunk);
    if (n === 0) {
      eof = true;
      continue;
    }
    const next = new Uint8Array(pending.length + n);
    next.set(pending);
    next.set(chunk.subarray(0, n), pending.length);
    pending = next;
  }
}

function format(value) {
  if (typeof value === "string") {
    return value;
  }
  try {
    return JSON.stringify(value);
  } catch {
    return String(value);
  }
}

function log(...args) {
  write(2, JSON.stringify({ log: args.map(format).join(" ") }));
}

globalThis.console = { log, info: log, warn: log, error: log, debug: log };

function* input() {
  for (let index = 0; ; index++) {
    const line = readLine();
    if (line === null) {
      current = null;
      return;
    }
    current = index;
    yield JSON.parse(line);
  }
}

try {
  const params = JSON.parse(readLine()) ?? undefined;
  for (const obj of applet(input(), params)) {
    if (obj === null || typeof obj !== "object" || Array.isArray(obj)) {
      throw new TypeError("applet must output objects, got " + format(obj));
    }
    write(1, JSON.stringify(obj));
  }
  write(2, JSON.stringify({ done: true }));
} catch (e) {
  write(2, JSON.stringify({
    error: e instanceof Error ? e.name + ": " + e.message : format(e),
    stack: e instanceof Error ? String(e.stack ?? "") : "",
    input: current,
  }));
}
