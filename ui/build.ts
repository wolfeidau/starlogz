import { mkdir, rm, writeFile } from "node:fs/promises";
import path from "node:path";

const outdir = "./internal/server/public";

await rm(outdir, { recursive: true, force: true });
await mkdir(outdir, { recursive: true });

// Bun inlines CSS url() fonts as data: URIs, which CSP font-src 'self' blocks.
// Emit hashed font files and point fonts.css at them with absolute URLs.
const fontPlugin: Bun.BunPlugin = {
  name: "font-files",
  setup(build) {
    build.onLoad({ filter: /styles[\\/]fonts\.css$/ }, async (args) => {
      let css = await Bun.file(args.path).text();
      for (const match of [...css.matchAll(/url\("([^"]+\.woff2)"\)/g)]) {
        const source = path.resolve(path.dirname(args.path), match[1]);
        const data = await Bun.file(source).bytes();
        const hash = new Bun.CryptoHasher("sha256")
          .update(data)
          .digest("hex")
          .slice(0, 8);
        const name = `${path.basename(source, ".woff2")}-${hash}.woff2`;
        await mkdir(path.join(outdir, "fonts"), { recursive: true });
        await Bun.write(path.join(outdir, "fonts", name), data);
        css = css.replace(match[0], `url("/public/fonts/${name}")`);
      }
      return { contents: css, loader: "css" };
    });
  },
};

const result = await Bun.build({
  entrypoints: ["./ui/dashboard.tsx"],
  outdir,
  target: "browser",
  format: "esm",
  splitting: true,
  plugins: [fontPlugin],
  external: ["/public/*"],
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
