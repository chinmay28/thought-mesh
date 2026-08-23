import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import { createBrowserRouter, RouterProvider } from 'react-router-dom';
import { AppLayout } from './app/AppLayout.tsx';
import { NotesPage } from './pages/NotesPage.tsx';
import { NotePage } from './pages/NotePage.tsx';
import { NewNotePage } from './pages/NewNotePage.tsx';
import { GraphPage } from './pages/GraphPage.tsx';
import { TodayPage } from './pages/TodayPage.tsx';
import { SyncPage } from './pages/SyncPage.tsx';
import './styles.css';

const router = createBrowserRouter([
  {
    path: '/',
    element: <AppLayout />,
    children: [
      { index: true, element: <NotesPage /> },
      { path: 'new', element: <NewNotePage /> },
      { path: 'graph', element: <GraphPage /> },
      { path: 'today', element: <TodayPage /> },
      { path: 'sync', element: <SyncPage /> },
      // The splat carries the note's vault path, slashes included.
      { path: 'notes/*', element: <NotePage /> },
    ],
  },
]);

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <RouterProvider router={router} />
  </StrictMode>,
);
