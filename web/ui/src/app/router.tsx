import { createBrowserRouter, Navigate } from 'react-router-dom'
import { AppShell } from './shell/AppShell'
import { RouteErrorBoundary } from './RouteErrorBoundary'

export const appRouter = createBrowserRouter([{
  element: <AppShell />,
  errorElement: <RouteErrorBoundary />,
  children: [
    { index: true, element: <Navigate to="/overview" replace /> },
    { path: 'overview', lazy: async () => ({ Component: (await import('../pages/OverviewPage')).OverviewPage }) },
    { path: 'sources', lazy: async () => ({ Component: (await import('../pages/SourcesPage')).SourcesPage }) },
    { path: 'integrations/zsxq-login', lazy: async () => ({ Component: (await import('../pages/ZSXQLoginPage')).ZSXQLoginPage }) },
    { path: 'channels', lazy: async () => ({ Component: (await import('../pages/ChannelsPage')).ChannelsPage }) },
    { path: 'deliveries', lazy: async () => ({ Component: (await import('../pages/DeliveriesPage')).DeliveriesPage }) },
    { path: 'history', lazy: async () => ({ Component: (await import('../pages/HistoryPage')).HistoryPage }) },
    { path: 'audit-logs', lazy: async () => ({ Component: (await import('../pages/AuditLogsPage')).AuditLogsPage }) },
    { path: 'ai', lazy: async () => ({ Component: (await import('../pages/AIWorkbenchPage')).AIWorkbenchPage }) },
    { path: 'ai-settings', lazy: async () => ({ Component: (await import('../pages/AISettingsPage')).AISettingsPage }) },
    { path: 'settings', lazy: async () => ({ Component: (await import('../pages/SettingsPage')).SettingsPage }) },
    { path: 'more', lazy: async () => ({ Component: (await import('../pages/MorePage')).MorePage }) },
    { path: '*', element: <Navigate to="/overview" replace /> },
  ],
}])
