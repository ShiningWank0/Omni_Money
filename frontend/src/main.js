import './runtime/compatibility.js'
import { createApp } from 'vue'
import { createPinia } from 'pinia'
import { initializeDesktopBridge } from './runtime/desktop-bridge.js'
import './assets/style.css'
import './assets/button-feedback.css'

async function startApplication() {
  await initializeDesktopBridge()
  const isLoginRoute = window.location.pathname === '/login' || window.location.pathname === '/login/'
  const { default: rootComponent } = await (isLoginRoute ? import('./views/LoginView.vue') : import('./App.vue'))
  createApp(rootComponent).use(createPinia()).mount('#app')
}

startApplication().catch(error => {
  console.error(error)
  document.getElementById('app').textContent = '画面を初期化できませんでした。アプリを再起動してください。'
})
