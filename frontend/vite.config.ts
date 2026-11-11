import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';

// Em desenvolvimento, /api é encaminhado para o backend Go.
export default defineConfig({
  plugins: [react()],
  server: {
    port: 3000,
    proxy: { '/api': 'http://localhost:8080' },
  },
});
