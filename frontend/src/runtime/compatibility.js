// Pinia 4 uses Object.hasOwn at runtime. Vite's es2019 target transforms
// syntax only, so older desktop WebViews still need this built-in.
if (typeof Object.hasOwn !== 'function') {
  Object.defineProperty(Object, 'hasOwn', {
    value(object, property) {
      return Object.prototype.hasOwnProperty.call(object, property)
    },
    writable: true,
    configurable: true,
  })
}
