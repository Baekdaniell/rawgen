import { copyFileSync, mkdirSync, rmSync } from "node:fs";
import { join } from "node:path";

const dist = join(process.cwd(), "dist");
rmSync(dist, { recursive: true, force: true });
mkdirSync(dist, { recursive: true });

for (const file of ["index.html", "styles.css", "app.js"]) {
  copyFileSync(join(process.cwd(), "src", file), join(dist, file));
}

