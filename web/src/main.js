import { createPinia } from 'pinia'
import { createApp } from 'vue'

import App from './App.vue'
import './assets/main.css'
import { UNAUTHORIZED_EVENT } from './lib/api'
import { installChunkReload } from './lib/reload'
import router from './router'
import { useAuthStore } from './stores/auth'
import { useThemeStore } from './stores/theme'

const app = createApp(App)
const pinia = createPinia()

app.use(pinia)
app.use(router)

useThemeStore(pinia).init()

installChunkReload()

// Any 401 (expired session, restarted server) sends the user to login.
window.addEventListener(UNAUTHORIZED_EVENT, () => {
  const auth = useAuthStore(pinia)
  if (!auth.loggedIn) return
  auth.invalidate()
  const current = router.currentRoute.value
  if (!current.meta.public) {
    router.push({ name: 'login', query: { redirect: current.fullPath } })
  }
})

app.mount('#app')
