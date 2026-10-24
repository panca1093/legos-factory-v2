import { createServer } from "node:http";
import { readFile } from "node:fs/promises";
const document = new URL("../../index.html", import.meta.url);
createServer(async (_req, res) => {
  res.setHeader("Content-Type", "text/html");
  res.end(await readFile(document));
}).listen(15174, "127.0.0.1");
