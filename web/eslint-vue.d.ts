// Read by lint only (tsconfig.eslint.json); vue-tsc reads the SFCs themselves.
declare module '*.vue' {
  import type { DefineComponent } from 'vue'
  const component: DefineComponent
  export default component
}
