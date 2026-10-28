# Presentation

Slidev deck published to GitHub Pages by `.github/workflows/build-presentation.yaml`.
The content lives in `slides.md`.

```bash
npm install
npm run dev      # local preview
npm run build    # static site in dist/ (gitignored)
npm run export   # PDF/PPTX export; needs a Playwright browser
```

## Dependency Audit

The deck is a static site; its dependencies run at build time and are not shipped to
readers.

- `dompurify` is overridden to `^3.4.17` in `package.json`. `monaco-editor` (via
  `@slidev/cli`) pinned 3.4.8, which is in the vulnerable range for the critical
  advisory; the override is a same-minor bump and the deck still builds.
- `image-size` 1.2.1 (high) and `katex` 0.16.47 (low) are pulled in by
  `pptxgenjs` and `mermaid`. `npm audit` only offers a downgrade of `@slidev/cli`
  from 53.0.0 to 52.6.0, which is a major downgrade and is not applied. Revisit when
  Slidev updates these transitive dependencies.

Run `npm audit` in this directory to re-check.
