import { readFileSync } from 'node:fs';
import { createRequire } from 'node:module';
import { dirname, join } from 'node:path';
import { defineConfig, type Plugin } from 'vite';
import react from '@vitejs/plugin-react';

const require = createRequire(import.meta.url);
const hlsPackageRoot = dirname(require.resolve('hls.js/package.json'));

function playbackHarnessAssets(): Plugin {
  return {
    name: 'itemba-playback-harness-assets',
    apply: 'build',
    generateBundle() {
      const hlsRuntime = readFileSync(join(hlsPackageRoot, 'dist', 'hls.light.min.js'));
      if (hlsRuntime.byteLength > 600 * 1024) this.error('The playback HLS runtime exceeds the 600 KiB asset limit.');
      this.emitFile({ type: 'asset', fileName: 'playback-hls.min.js', source: hlsRuntime });
      this.emitFile({ type: 'asset', fileName: 'playback-hls.LICENSE.txt', source: readFileSync(join(hlsPackageRoot, 'LICENSE')) });
    },
  };
}

export default defineConfig({
  plugins: [react(), playbackHarnessAssets()],
  server: {
    port: 5173,
    proxy: {
      '/api': {
        target: 'http://127.0.0.1:8080',
        changeOrigin: true,
        ws: true,
      },
      '/testbench': {
        target: 'http://127.0.0.1:8080',
        changeOrigin: true,
      },
    },
  },
});
