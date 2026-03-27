/** @type {import('tailwindcss').Config} */
const config = {
  darkMode: 'class',
  content: ['./src/**/*.{html,js,svelte,ts}'],
  theme: {
    extend: {
      colors: {
        base: '#0A0A0F',
        surface: '#141420',
        elevated: '#1E1E2E',
        'border-default': '#2A2A3C',
        'text-primary': '#E4E4ED',
        'text-secondary': '#8888A0',
        'text-muted': '#555568',
        accent: {
          DEFAULT: '#3B82F6',
          hover: '#2563EB'
        }
      },
      fontFamily: {
        sans: ['Inter', 'system-ui', 'sans-serif'],
        mono: ['JetBrains Mono', 'monospace']
      }
    }
  },
  plugins: []
};

export default config;
