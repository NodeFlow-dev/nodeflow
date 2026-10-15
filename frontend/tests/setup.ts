// Loaded via `node --import` before every test file: src/ uses extensionless
// relative imports (Vite/Bundler resolution); map them to the .ts files so
// node --experimental-strip-types can load modules that import src/i18n.
import { registerHooks } from 'node:module';

registerHooks({
  resolve(specifier, context, nextResolve) {
    try {
      return nextResolve(specifier, context);
    } catch (error) {
      if (specifier.startsWith('.') && !/\.[cm]?[jt]sx?$/.test(specifier)) return nextResolve(`${specifier}.ts`, context);
      throw error;
    }
  },
});
