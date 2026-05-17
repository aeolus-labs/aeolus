import React from 'react'
import ReactDOM from 'react-dom/client'
import App from './App'
import { DashboardStateProvider } from './state'
import './styles.css'

ReactDOM.createRoot(document.getElementById('root')!).render(
  <React.StrictMode>
    <DashboardStateProvider>
      <App />
    </DashboardStateProvider>
  </React.StrictMode>,
)
