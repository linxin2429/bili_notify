import { createBrowserRouter, Navigate } from 'react-router-dom'
import { AppShell } from './shell/AppShell'
import { RouteErrorBoundary } from './RouteErrorBoundary'

export const appRouter = createBrowserRouter([{
  element: <AppShell />,
  errorElement: <RouteErrorBoundary />,
  children: [
    { index: true, element: <Navigate to="/overview" replace /> },
    { path: 'overview', lazy: async () => ({ Component: (await import('../pages/OverviewPage')).OverviewPage }) },
    { path: 'ups', lazy: async () => ({ Component: (await import('../pages/UPsPage')).UPsPage }) },
    { path: 'channels', lazy: async () => ({ Component: (await import('../pages/ChannelsPage')).ChannelsPage }) },
    { path: 'deliveries', lazy: async () => ({ Component: (await import('../pages/DeliveriesPage')).DeliveriesPage }) },
    { path: 'history', lazy: async () => ({ Component: (await import('../pages/HistoryPage')).HistoryPage }) },
    { path: 'audit-logs', lazy: async () => ({ Component: (await import('../pages/AuditLogsPage')).AuditLogsPage }) },
    { path: 'settings', lazy: async () => ({ Component: (await import('../pages/SettingsPage')).SettingsPage }) },
    { path: '*', element: <Navigate to="/overview" replace /> },
  ],
}])
