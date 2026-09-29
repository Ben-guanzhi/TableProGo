import React from 'react'
import ReactDOM from 'react-dom/client'
import App from './App'
import { preapplyTheme } from './theme'
import './index.css'

preapplyTheme()

ReactDOM.createRoot(document.getElementById('root')!).render(
  <React.StrictMode>
    <App />
  </React.StrictMode>,
)
