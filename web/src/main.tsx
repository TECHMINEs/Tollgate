import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import './index.css'
import App from './App.tsx'
import BrandBar from './components/brand/BrandBar'
import BrandFooter from './components/brand/BrandFooter'

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <BrandBar project="Tollgate" />
    <App />
    <BrandFooter />
  </StrictMode>,
)
