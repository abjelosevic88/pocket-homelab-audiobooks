import React, { lazy, Suspense } from 'react'
import ReactDOM from 'react-dom/client'
import { BrowserRouter, Navigate, Outlet, Route, Routes, useLocation } from 'react-router-dom'
import './styles/base.css'
import './styles/app.css'
import './styles/shared.css'
import './styles/books.css'
import './styles/settings.css'
import { AppProvider, useApp } from './lib/app-context'
import { PlayerProvider } from './lib/player'
import { Loading, ToastProvider } from './components/ui'
import Layout from './components/Layout'
import { ErrorBoundary } from './components/ErrorBoundary'
import Login from './pages/Login'
import Home from './pages/Home'
import Library from './pages/Library'
import BookPage from './pages/BookPage'
import SeriesPage from './pages/SeriesPage'
import AuthorPage from './pages/AuthorPage'
import UpNext from './pages/UpNext'
import History from './pages/History'
import Stats from './pages/Stats'
import Lists from './pages/Lists'
import Shares from './pages/Shares'
import Trash from './pages/Trash'
import Podcasts from './pages/Podcasts'
import Settings from './pages/Settings'

const Reader = lazy(() => import('./pages/Reader'))

function Gate({ children }: { children: React.ReactNode }) {
  const { status, user } = useApp()
  const loc = useLocation()
  if (!status) return <div className="boot"><img src="/favicon.svg" alt="" /><Loading /></div>
  if (status.needs_setup) return <Navigate to="/setup" replace />
  if (!user) return <Navigate to="/login" replace state={{ from: loc.pathname + loc.search }} />
  return <>{children}</>
}

// one player for the whole signed-in app: it keeps playing between pages and in Settings
function Signed() {
  return <Gate><PlayerProvider><Outlet /></PlayerProvider></Gate>
}

function App() {
  return (
    <Routes>
      <Route path="/login" element={<Login mode="login" />} />
      <Route path="/setup" element={<Login mode="setup" />} />
      <Route element={<Signed />}>
        <Route path="/read/:id" element={<ErrorBoundary where="reader"><Suspense fallback={<Loading />}><Reader /></Suspense></ErrorBoundary>} />
        <Route path="/settings/:tab?" element={<ErrorBoundary where="settings"><Settings /></ErrorBoundary>} />
        <Route element={<ErrorBoundary where="audiobooks"><Layout /></ErrorBoundary>}>
          <Route path="/" element={<Home />} />
          <Route path="/library" element={<Library view="books" key="books" />} />
          <Route path="/library/series" element={<Library view="series" key="series" />} />
          <Route path="/library/authors" element={<Library view="authors" key="authors" />} />
          <Route path="/book/:id" element={<BookPage />} />
          <Route path="/series/:id" element={<SeriesPage />} />
          <Route path="/author/:id" element={<AuthorPage />} />
          <Route path="/up-next" element={<UpNext />} />
          <Route path="/history" element={<History />} />
          <Route path="/stats" element={<Stats />} />
          <Route path="/shares" element={<Shares />} />
          <Route path="/trash" element={<Trash />} />
          <Route path="/lists" element={<Lists />} />
          <Route path="/podcasts/:id?" element={<Podcasts />} />
          <Route path="*" element={<Navigate to="/" replace />} />
        </Route>
      </Route>
    </Routes>
  )
}

// an older build may have registered a service worker: take it away
navigator.serviceWorker?.getRegistrations?.().then(rs => rs.forEach(r => r.unregister())).catch(() => {})

ReactDOM.createRoot(document.getElementById('root')!).render(
  <React.StrictMode>
    <BrowserRouter>
      <ErrorBoundary where="app"><ToastProvider><AppProvider><App /></AppProvider></ToastProvider></ErrorBoundary>
    </BrowserRouter>
  </React.StrictMode>,
)
