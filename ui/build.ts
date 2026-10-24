import { mkdir, rm, writeFile } from "node:fs/promises";
import path from "node:path";

const outdir = "./internal/server/public";

await rm(outdir, { recursive: true, force: true });
await mkdir(outdir, { recursive: true });

const result = await Bun.build({
  entrypoints: ["./ui/dashboard.tsx"],
  outdir,
  target: "browser",
  format: "esm",
  splitting: true,
  minify: true,
  define: { "process.env.NODE_ENV": JSON.stringify("production") },
  naming: {
    entry: "[name]-[hash].[ext]",
    chunk: "[name]-[hash].[ext]",
    asset: "[name]-[hash].[ext]",
  },
});

if (!result.success) {
  for (const log of result.logs) console.error(log);
  process.exit(1);
}

const script = result.outputs.find((o) => o.kind === "entry-point");
if (!script) {
  console.error("build produced no entry-point output");
  process.exit(1);
}

// Every stylesheet loads up front so CSS from lazy chunks never flashes unstyled.
const styles = result.outputs
  .filter((o) => o.path.endsWith(".css"))
  .map((o) => path.basename(o.path))
  .sort();

await writeFile(
  path.join(outdir, "manifest.json"),
  `${JSON.stringify({ script: path.basename(script.path), styles }, null, 2)}\n`,
);
