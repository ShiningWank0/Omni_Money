import './runtime/compatibility.js'
import { createApp } from 'vue'
import { createPinia } from 'pinia'
import App from './App.vue'
import LoginView from './views/LoginView.vue'
import './assets/style.css'
import './assets/button-feedback.css'

const isLoginRoute = window.location.pathname === '/login' || window.location.pathname === '/login/'
const rootComponent = isLoginRoute ? LoginView : App
const app = createApp(rootComponent)
const pinia = createPinia()

app.use(pinia)
app.mount('#app')
