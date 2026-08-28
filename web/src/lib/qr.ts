// QR Code generation (ISO/IEC 18004), byte mode, error-correction level M.
//
// The Panel encodes exactly one thing: the UTF-8 bytes of a Connection
// profile's canonical VLESS URI (IN-DEV-SPEC §6.3). That is why this module
// takes bytes rather than a string — there is no place to slip in an
// alternate serialization, a wrapper, or trimmed whitespace between the URI
// the card displays and the symbol a client scans.

export interface QrCode {
  // size is the symbol's width in modules; modules is size × size, indexed
  // [row][column], true where a module is dark.
  readonly size: number;
  readonly modules: readonly (readonly boolean[])[];
}

// The byte-mode segment header.
const MODE_BYTE = 0b0100;

// The light margin a scanner needs around the symbol to find it. Callers that
// draw a symbol are responsible for leaving it.
export const QUIET_ZONE_MODULES = 4;

// Error-correction level M: ~15% recovery, the level share links are
// conventionally generated at. Both tables are indexed by version 1–40.
const ECC_LEVEL_BITS = 0b00;
const ECC_CODEWORDS_PER_BLOCK = [
  -1, 10, 16, 26, 18, 24, 16, 18, 22, 22, 26, 30, 22, 22, 24, 24, 28, 28, 26, 26, 26, 26, 28, 28,
  28, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28, 28,
];
const ECC_BLOCKS = [
  -1, 1, 1, 1, 2, 2, 4, 4, 4, 5, 5, 5, 8, 9, 9, 10, 10, 11, 13, 14, 16, 17, 17, 18, 20, 21, 23, 25,
  26, 28, 29, 31, 33, 35, 37, 38, 40, 43, 45, 47, 49,
];
const MIN_VERSION = 1;
const MAX_VERSION = 40;

// Penalty weights for the four mask-scoring rules (§7.8.3).
const PENALTY_RUN = 3;
const PENALTY_BLOCK = 3;
const PENALTY_FINDER_LIKE = 40;
const PENALTY_IMBALANCE = 10;

// encodeQr returns the smallest symbol carrying these exact bytes.
export function encodeQr(bytes: Uint8Array): QrCode {
  const version = smallestVersion(bytes.length);
  return buildSymbol(interleave(dataCodewordsFor(bytes, version), version), version);
}

// qrPathData draws the dark modules as one SVG path in module coordinates:
// one horizontal run per subpath, which keeps a dense symbol to a single
// element instead of a few thousand rects.
export function qrPathData(code: QrCode): string {
  const runs: string[] = [];
  for (let row = 0; row < code.size; row++) {
    let column = 0;
    while (column < code.size) {
      if (!code.modules[row][column]) {
        column++;
        continue;
      }
      const start = column;
      while (column < code.size && code.modules[row][column]) column++;
      runs.push(`M${start} ${row}h${column - start}v1h-${column - start}z`);
    }
  }
  return runs.join("");
}

// smallestVersion picks the first symbol whose data capacity holds the
// payload, its mode header, and its length field.
function smallestVersion(byteLength: number): number {
  for (let version = MIN_VERSION; version <= MAX_VERSION; version++) {
    const capacity = dataCodewordCount(version) * 8;
    if (4 + characterCountBits(version) + byteLength * 8 <= capacity) return version;
  }
  throw new Error(`payload of ${byteLength} bytes is too long for a QR symbol`);
}

// Versions 1–9 carry an 8-bit payload length, 10–40 a 16-bit one.
function characterCountBits(version: number): number {
  return version <= 9 ? 8 : 16;
}

// rawCodewordCount is the symbol's total capacity: the data-carrying modules
// left once the finder, alignment, timing, format, and version patterns take
// their share (§7.5.1 count, byte-aligned).
function rawCodewordCount(version: number): number {
  let modules = (16 * version + 128) * version + 64;
  if (version >= 2) {
    const alignments = Math.floor(version / 7) + 2;
    modules -= (25 * alignments - 10) * alignments - 55;
    if (version >= 7) modules -= 36;
  }
  return Math.floor(modules / 8);
}

function dataCodewordCount(version: number): number {
  return rawCodewordCount(version) - ECC_CODEWORDS_PER_BLOCK[version] * ECC_BLOCKS[version];
}

// dataCodewordsFor lays the payload out as the bit stream §7.4 specifies —
// mode, length, bytes, terminator — then pads to the version's full data
// capacity with the alternating 0xEC/0x11 filler.
function dataCodewordsFor(bytes: Uint8Array, version: number): Uint8Array {
  const bits: number[] = [];
  appendBits(bits, MODE_BYTE, 4);
  appendBits(bits, bytes.length, characterCountBits(version));
  for (const byte of bytes) appendBits(bits, byte, 8);

  const capacity = dataCodewordCount(version) * 8;
  for (let index = 0; index < 4 && bits.length < capacity; index++) bits.push(0);
  while (bits.length % 8 !== 0) bits.push(0);

  const codewords = new Uint8Array(dataCodewordCount(version));
  for (let index = 0; index < bits.length; index++) {
    codewords[index >>> 3] |= bits[index] << (7 - (index & 7));
  }
  for (let index = bits.length / 8, pad = 0xec; index < codewords.length; index++, pad ^= 0xfd) {
    codewords[index] = pad;
  }
  return codewords;
}

function appendBits(bits: number[], value: number, length: number): void {
  for (let index = length - 1; index >= 0; index--) bits.push((value >>> index) & 1);
}

// interleave splits the data into the version's blocks, appends each block's
// Reed-Solomon codewords, and interleaves both halves in the order §7.6
// requires — the arrangement that lets a scanner lose a whole region of the
// symbol and still recover every block.
function interleave(data: Uint8Array, version: number): Uint8Array {
  const blockCount = ECC_BLOCKS[version];
  const eccLength = ECC_CODEWORDS_PER_BLOCK[version];
  const rawCodewords = rawCodewordCount(version);
  const shortBlockLength = Math.floor(rawCodewords / blockCount);
  const shortBlocks = blockCount - (rawCodewords % blockCount);
  const divisor = reedSolomonDivisor(eccLength);

  const dataBlocks: Uint8Array[] = [];
  const eccBlocks: Uint8Array[] = [];
  for (let index = 0, read = 0; index < blockCount; index++) {
    const length = shortBlockLength - eccLength + (index < shortBlocks ? 0 : 1);
    const block = data.subarray(read, read + length);
    read += length;
    dataBlocks.push(block);
    eccBlocks.push(reedSolomonRemainder(block, divisor));
  }

  const result = new Uint8Array(rawCodewords);
  let written = 0;
  const longestBlock = shortBlockLength - eccLength + 1;
  for (let index = 0; index < longestBlock; index++) {
    for (const block of dataBlocks) {
      if (index < block.length) result[written++] = block[index];
    }
  }
  for (let index = 0; index < eccLength; index++) {
    for (const block of eccBlocks) result[written++] = block[index];
  }
  return result;
}

// reedSolomonDivisor returns the generator polynomial of the given degree,
// its coefficients in descending order with the leading 1 left implicit.
function reedSolomonDivisor(degree: number): Uint8Array {
  const coefficients = new Uint8Array(degree);
  coefficients[degree - 1] = 1;
  let root = 1;
  for (let index = 0; index < degree; index++) {
    for (let term = 0; term < degree; term++) {
      coefficients[term] = multiply(coefficients[term], root);
      if (term + 1 < degree) coefficients[term] ^= coefficients[term + 1];
    }
    root = multiply(root, 2);
  }
  return coefficients;
}

function reedSolomonRemainder(data: Uint8Array, divisor: Uint8Array): Uint8Array {
  const result = new Uint8Array(divisor.length);
  for (const byte of data) {
    const factor = byte ^ result[0];
    result.copyWithin(0, 1);
    result[result.length - 1] = 0;
    for (let index = 0; index < divisor.length; index++) {
      result[index] ^= multiply(divisor[index], factor);
    }
  }
  return result;
}

// multiply in GF(2^8) modulo the QR field's primitive polynomial x^8 + x^4 +
// x^3 + x^2 + 1.
function multiply(left: number, right: number): number {
  let product = 0;
  for (let bit = 7; bit >= 0; bit--) {
    product = (product << 1) ^ ((product >>> 7) * 0x11d);
    product ^= ((right >>> bit) & 1) * left;
  }
  return product & 0xff;
}

interface Canvas {
  size: number;
  modules: boolean[][];
  // reserved marks the function patterns: they are drawn once and neither
  // data placement nor masking may touch them.
  reserved: boolean[][];
}

// buildSymbol draws the function patterns, threads the codewords through the
// remaining modules, then keeps whichever of the eight masks scores best.
function buildSymbol(codewords: Uint8Array, version: number): QrCode {
  const size = version * 4 + 17;
  const canvas: Canvas = {
    size,
    modules: emptyGrid(size),
    reserved: emptyGrid(size),
  };

  drawFunctionPatterns(canvas, version);
  drawCodewords(canvas, codewords);

  let bestMask = 0;
  let bestPenalty = Infinity;
  for (let mask = 0; mask < 8; mask++) {
    applyMask(canvas, mask);
    drawFormatBits(canvas, mask);
    const penalty = penaltyScore(canvas);
    if (penalty < bestPenalty) {
      bestPenalty = penalty;
      bestMask = mask;
    }
    applyMask(canvas, mask); // masking is its own inverse
  }
  applyMask(canvas, bestMask);
  drawFormatBits(canvas, bestMask);

  return { size, modules: canvas.modules };
}

function emptyGrid(size: number): boolean[][] {
  return Array.from({ length: size }, () => new Array<boolean>(size).fill(false));
}

function setFunctionModule(canvas: Canvas, x: number, y: number, dark: boolean): void {
  canvas.modules[y][x] = dark;
  canvas.reserved[y][x] = true;
}

function drawFunctionPatterns(canvas: Canvas, version: number): void {
  const last = canvas.size - 1;

  // Timing patterns: the alternating row and column scanners use to measure
  // the module pitch.
  for (let index = 0; index < canvas.size; index++) {
    setFunctionModule(canvas, 6, index, index % 2 === 0);
    setFunctionModule(canvas, index, 6, index % 2 === 0);
  }

  // Finders in three corners, drawn with their separators.
  drawFinderPattern(canvas, 3, 3);
  drawFinderPattern(canvas, last - 3, 3);
  drawFinderPattern(canvas, 3, last - 3);

  const positions = alignmentPatternPositions(version);
  for (let row = 0; row < positions.length; row++) {
    for (let column = 0; column < positions.length; column++) {
      // The three finder corners have no alignment pattern.
      const corner =
        (row === 0 && column === 0) ||
        (row === 0 && column === positions.length - 1) ||
        (row === positions.length - 1 && column === 0);
      if (!corner) drawAlignmentPattern(canvas, positions[column], positions[row]);
    }
  }

  // Reserve the format area with a placeholder; the real bits depend on the
  // mask, chosen after the data is placed.
  drawFormatBits(canvas, 0);
  drawVersionBits(canvas, version);
}

function drawFinderPattern(canvas: Canvas, centerX: number, centerY: number): void {
  for (let offsetY = -4; offsetY <= 4; offsetY++) {
    for (let offsetX = -4; offsetX <= 4; offsetX++) {
      const x = centerX + offsetX;
      const y = centerY + offsetY;
      if (x < 0 || x >= canvas.size || y < 0 || y >= canvas.size) continue;
      // Concentric rings: dark 7×7 border, light ring, dark 3×3 core. The
      // outermost ring at distance 4 is the separator.
      const distance = Math.max(Math.abs(offsetX), Math.abs(offsetY));
      setFunctionModule(canvas, x, y, distance !== 2 && distance !== 4);
    }
  }
}

function drawAlignmentPattern(canvas: Canvas, centerX: number, centerY: number): void {
  for (let offsetY = -2; offsetY <= 2; offsetY++) {
    for (let offsetX = -2; offsetX <= 2; offsetX++) {
      const distance = Math.max(Math.abs(offsetX), Math.abs(offsetY));
      setFunctionModule(canvas, centerX + offsetX, centerY + offsetY, distance !== 1);
    }
  }
}

// alignmentPatternPositions spreads the pattern centers evenly (§6.3.5): the
// first at 6, the last 7 modules from the edge, the rest at an even step.
function alignmentPatternPositions(version: number): number[] {
  if (version === 1) return [];
  const count = Math.floor(version / 7) + 2;
  const step = version === 32 ? 26 : Math.ceil((version * 4 + 4) / (count * 2 - 2)) * 2;
  const positions = [6];
  for (let position = version * 4 + 10; positions.length < count; position -= step) {
    positions.splice(1, 0, position);
  }
  return positions;
}

// drawFormatBits writes the error-correction level and mask, protected by a
// (15,5) BCH code and the fixed 0x5412 mask, into both format areas.
function drawFormatBits(canvas: Canvas, mask: number): void {
  const data = (ECC_LEVEL_BITS << 3) | mask;
  let remainder = data;
  for (let index = 0; index < 10; index++) {
    remainder = (remainder << 1) ^ ((remainder >>> 9) * 0x537);
  }
  const bits = ((data << 10) | remainder) ^ 0x5412;

  // First copy: down the left of the top-left finder, then back along its
  // bottom row.
  for (let index = 0; index <= 5; index++) setFunctionModule(canvas, 8, index, bitAt(bits, index));
  setFunctionModule(canvas, 8, 7, bitAt(bits, 6));
  setFunctionModule(canvas, 8, 8, bitAt(bits, 7));
  setFunctionModule(canvas, 7, 8, bitAt(bits, 8));
  for (let index = 9; index < 15; index++) {
    setFunctionModule(canvas, 14 - index, 8, bitAt(bits, index));
  }

  // Second copy: split between the other two finders.
  for (let index = 0; index < 8; index++) {
    setFunctionModule(canvas, canvas.size - 1 - index, 8, bitAt(bits, index));
  }
  for (let index = 8; index < 15; index++) {
    setFunctionModule(canvas, 8, canvas.size - 15 + index, bitAt(bits, index));
  }
  setFunctionModule(canvas, 8, canvas.size - 8, true); // always dark
}

// drawVersionBits writes the version number and its (18,6) BCH check into
// the two blocks version 7 and above carry.
function drawVersionBits(canvas: Canvas, version: number): void {
  if (version < 7) return;
  let remainder = version;
  for (let index = 0; index < 12; index++) {
    remainder = (remainder << 1) ^ ((remainder >>> 11) * 0x1f25);
  }
  const bits = (version << 12) | remainder;
  for (let index = 0; index < 18; index++) {
    const bit = bitAt(bits, index);
    const far = canvas.size - 11 + (index % 3);
    const near = Math.floor(index / 3);
    setFunctionModule(canvas, far, near, bit);
    setFunctionModule(canvas, near, far, bit);
  }
}

function bitAt(value: number, index: number): boolean {
  return ((value >>> index) & 1) !== 0;
}

// drawCodewords threads the bit stream through every non-reserved module,
// two columns at a time from the bottom right, alternating upward and
// downward (§7.7.3).
function drawCodewords(canvas: Canvas, codewords: Uint8Array): void {
  let bit = 0;
  for (let right = canvas.size - 1; right >= 1; right -= 2) {
    // Column 6 is the vertical timing pattern; the pairing skips over it.
    if (right === 6) right = 5;
    for (let step = 0; step < canvas.size; step++) {
      for (let column = 0; column < 2; column++) {
        const x = right - column;
        const upward = ((right + 1) & 2) === 0;
        const y = upward ? canvas.size - 1 - step : step;
        if (canvas.reserved[y][x] || bit >= codewords.length * 8) continue;
        canvas.modules[y][x] = bitAt(codewords[bit >>> 3], 7 - (bit & 7));
        bit++;
      }
    }
  }
}

// applyMask inverts the data modules the given mask selects. Applying the
// same mask twice restores the symbol, which is how mask selection tries
// each candidate on one canvas.
function applyMask(canvas: Canvas, mask: number): void {
  for (let y = 0; y < canvas.size; y++) {
    for (let x = 0; x < canvas.size; x++) {
      if (!canvas.reserved[y][x] && maskBit(mask, x, y)) {
        canvas.modules[y][x] = !canvas.modules[y][x];
      }
    }
  }
}

function maskBit(mask: number, x: number, y: number): boolean {
  switch (mask) {
    case 0:
      return (x + y) % 2 === 0;
    case 1:
      return y % 2 === 0;
    case 2:
      return x % 3 === 0;
    case 3:
      return (x + y) % 3 === 0;
    case 4:
      return (Math.floor(x / 3) + Math.floor(y / 2)) % 2 === 0;
    case 5:
      return ((x * y) % 2) + ((x * y) % 3) === 0;
    case 6:
      return (((x * y) % 2) + ((x * y) % 3)) % 2 === 0;
    default:
      return ((((x + y) % 2) + ((x * y) % 3)) % 2) === 0;
  }
}

// penaltyScore rates a masked symbol by the four rules in §7.8.3 — long
// same-color runs, solid 2×2 blocks, finder-lookalikes, and an unbalanced
// dark/light ratio. The lowest score wins.
function penaltyScore(canvas: Canvas): number {
  let score = 0;
  const size = canvas.size;

  for (let line = 0; line < size; line++) {
    for (const horizontal of [true, false]) {
      const at = (index: number) =>
        horizontal ? canvas.modules[line][index] : canvas.modules[index][line];
      let runColor = false;
      let runLength = 0;
      const history = [0, 0, 0, 0, 0, 0, 0];
      for (let index = 0; index < size; index++) {
        if (at(index) === runColor) {
          runLength++;
          if (runLength === 5) score += PENALTY_RUN;
          else if (runLength > 5) score++;
          continue;
        }
        addRunHistory(history, runLength, size);
        if (!runColor) score += countFinderLookalikes(history) * PENALTY_FINDER_LIKE;
        runColor = at(index);
        runLength = 1;
      }
      if (runColor) {
        addRunHistory(history, runLength, size);
        runLength = 0;
      }
      addRunHistory(history, runLength + size, size);
      score += countFinderLookalikes(history) * PENALTY_FINDER_LIKE;
    }
  }

  for (let y = 0; y < size - 1; y++) {
    for (let x = 0; x < size - 1; x++) {
      const color = canvas.modules[y][x];
      if (
        color === canvas.modules[y][x + 1] &&
        color === canvas.modules[y + 1][x] &&
        color === canvas.modules[y + 1][x + 1]
      ) {
        score += PENALTY_BLOCK;
      }
    }
  }

  let dark = 0;
  for (const row of canvas.modules) {
    for (const module of row) if (module) dark++;
  }
  const total = size * size;
  // One penalty step per 5% the dark share strays from half.
  score += (Math.ceil(Math.abs(dark * 20 - total * 10) / total) - 1) * PENALTY_IMBALANCE;
  return score;
}

// addRunHistory records one finished run, treating the symbol's edge as an
// unbounded light border so a finder pattern touching it still counts.
function addRunHistory(history: number[], runLength: number, size: number): void {
  if (history[0] === 0) runLength += size;
  history.pop();
  history.unshift(runLength);
}

// countFinderLookalikes reports how many 1:1:3:1:1 finder-like sequences,
// with the required light margin, end at the current position.
function countFinderLookalikes(history: number[]): number {
  const unit = history[1];
  const core =
    unit > 0 &&
    history[2] === unit &&
    history[3] === unit * 3 &&
    history[4] === unit &&
    history[5] === unit;
  return (
    (core && history[0] >= unit * 4 && history[6] >= unit ? 1 : 0) +
    (core && history[6] >= unit * 4 && history[0] >= unit ? 1 : 0)
  );
}
