import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { BrowserRouter } from 'react-router'
import { App } from './App'
import { Toaster } from './components/Toaster'
import './index.css'

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      retry: (count, err) => count < 2 && !(err instanceof Error && 'status' in err && (err as { status: number }).status >= 400),
      refetchOnWindowFocus: true,
      staleTime: 2000,
    },
  },
})

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <BrowserRouter>
        <Toaster>
          <App />
        </Toaster>
      </BrowserRouter>
    </QueryClientProvider>
  </StrictMode>,
)
