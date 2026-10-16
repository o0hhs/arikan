/**
 * The Material colour utilities ship an ESM build whose internal imports are extensionless
 * (`./cam16`), which a bundler resolves and plain Node does not. This hook retries a failing
 * relative specifier with ".js", which is enough for the package, and is registered by
 * scripts/gen-m3-themes.mjs before it imports anything from it.
 */
export async function resolve(specifier, context, next) {
  try {
    return await next(specifier, context);
  } catch (e) {
    if (specifier.startsWith(".") && !/\.[cm]?js$/.test(specifier)) return next(`${specifier}.js`, context);
    throw e;
  }
}
