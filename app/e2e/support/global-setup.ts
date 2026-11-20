// Fails early, with the command to run, when the binary under test is missing.
import fs from 'node:fs';
import path from 'node:path';
import { BINARY, WEB_DIST } from './server';

export default function globalSetup() {
  for (const file of [BINARY, path.join(WEB_DIST, 'index.html')]) {
    if (!fs.existsSync(file)) {
      throw new Error(
        `${file} is missing: run \`make e2e\` (it builds the app and the binary first)`
      );
    }
  }
}
