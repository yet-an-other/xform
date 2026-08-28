// Test-side QR scanner: paints a module matrix the way a phone camera sees
// it — dark and light squares inside the standard quiet zone — and reads it
// back with an independent decoder. Nothing here shares code with the
// encoder, so a round trip through it is real evidence about the bytes a
// scanner recovers, not a restatement of how they were written.
import jsQR from "jsqr";

const QUIET_ZONE_MODULES = 4;
const PIXELS_PER_MODULE = 4;

export type QrModules = readonly (readonly boolean[])[];

// rasterize paints one matrix into RGBA pixels: dark modules black, light
// modules and the quiet zone white.
export function rasterize(modules: QrModules): {
  data: Uint8ClampedArray;
  width: number;
  height: number;
} {
  const size = modules.length + QUIET_ZONE_MODULES * 2;
  const width = size * PIXELS_PER_MODULE;
  const data = new Uint8ClampedArray(width * width * 4).fill(255);
  for (let row = 0; row < modules.length; row++) {
    for (let column = 0; column < modules.length; column++) {
      if (!modules[row][column]) continue;
      const top = (row + QUIET_ZONE_MODULES) * PIXELS_PER_MODULE;
      const left = (column + QUIET_ZONE_MODULES) * PIXELS_PER_MODULE;
      for (let y = top; y < top + PIXELS_PER_MODULE; y++) {
        for (let x = left; x < left + PIXELS_PER_MODULE; x++) {
          const pixel = (y * width + x) * 4;
          data[pixel] = 0;
          data[pixel + 1] = 0;
          data[pixel + 2] = 0;
        }
      }
    }
  }
  return { data, width, height: width };
}

// decodeQr scans a rendered matrix and returns the bytes it carries.
export function decodeQr(modules: QrModules): Uint8Array {
  const image = rasterize(modules);
  const result = jsQR(image.data, image.width, image.height);
  if (result === null) {
    throw new Error("no QR code found in the rendered matrix");
  }
  return Uint8Array.from(result.binaryData);
}

// modulesFromPathData rebuilds the matrix a rendered <path> draws, so a test
// can scan exactly what the DOM shows rather than what the encoder returned.
export function modulesFromPathData(pathData: string, size: number): boolean[][] {
  const modules = Array.from({ length: size }, () => new Array<boolean>(size).fill(false));
  const runs = pathData.matchAll(/M(\d+) (\d+)h(\d+)v1h-\3z/g);
  let matched = 0;
  for (const [, column, row, length] of runs) {
    matched++;
    for (let offset = 0; offset < Number(length); offset++) {
      modules[Number(row)][Number(column) + offset] = true;
    }
  }
  if (matched === 0) {
    throw new Error(`no module runs in path data: ${pathData.slice(0, 80)}`);
  }
  return modules;
}
