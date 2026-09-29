/** @type {import('tailwindcss').Config} */
export default {
  content: [
    "./index.html",
    "./src/**/*.{js,ts,jsx,tsx}",
  ],
  theme: {
    extend: {
      // Dense UI text is specced in rem, not px, so it still scales with the
      // browser's root font size. `2xs` keeps the platform's 11px size while
      // inheriting line height, which is what the row-height-dependent
      // surfaces (grid, sidebar) rely on.
      fontSize: {
        '2xs': ['0.6875rem', { lineHeight: 'inherit' }],
      },
      // Colours come from the semantic tokens in index.css so the whole UI —
      // including the CodeMirror theme — can be repainted by one attribute.
      // Values are "r g b" triplets, which keeps alpha modifiers working.
      colors: {
        canvas: 'rgb(var(--canvas) / <alpha-value>)',
        panel: 'rgb(var(--panel) / <alpha-value>)',
        raised: 'rgb(var(--raised) / <alpha-value>)',
        emphasis: 'rgb(var(--emphasis) / <alpha-value>)',
        line: 'rgb(var(--line) / <alpha-value>)',
        'line-soft': 'rgb(var(--line-soft) / <alpha-value>)',
        'line-strong': 'rgb(var(--line-strong) / <alpha-value>)',
        ink: 'rgb(var(--ink) / <alpha-value>)',
        secondary: 'rgb(var(--secondary) / <alpha-value>)',
        muted: 'rgb(var(--muted) / <alpha-value>)',
        accent: 'rgb(var(--accent) / <alpha-value>)',
        'accent-hover': 'rgb(var(--accent-hover) / <alpha-value>)',
        success: 'rgb(var(--success) / <alpha-value>)',
        'success-hover': 'rgb(var(--success-hover) / <alpha-value>)',
        danger: 'rgb(var(--danger) / <alpha-value>)',
        'danger-hover': 'rgb(var(--danger-hover) / <alpha-value>)',
        warning: 'rgb(var(--warning) / <alpha-value>)',
        'on-accent': 'rgb(var(--on-accent) / <alpha-value>)',
        'on-success': 'rgb(var(--on-success) / <alpha-value>)',
        'on-danger': 'rgb(var(--on-danger) / <alpha-value>)',
        scrim: 'rgb(var(--scrim) / <alpha-value>)',
        'icon-db': 'rgb(var(--icon-db) / <alpha-value>)',
        'icon-view': 'rgb(var(--icon-view) / <alpha-value>)',
        'row-alt': 'rgb(var(--row-alt) / <alpha-value>)',
      },
    },
  },
  plugins: [],
}
