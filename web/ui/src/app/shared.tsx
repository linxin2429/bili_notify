import type React from 'react'
import { Avatar, Box, Paper, Stack, Typography } from '@mui/material'
import type { DashboardSnapshot } from '../types'

export type SnapshotMutation<T> = (snapshot: DashboardSnapshot, value: T) => DashboardSnapshot
export type RunMutation = <T>(request: () => Promise<T>, update?: SnapshotMutation<T>) => Promise<T>

export function PageHeader({ title, subtitle, action }: { title: string; subtitle: string; action?: React.ReactNode }) {
  return <Stack direction={{ xs: 'column', sm: 'row' }} justifyContent="space-between" alignItems={{ xs: 'stretch', sm: 'center' }} gap={2}>
    <Box><Typography component="h1" variant="h4" fontWeight={850}>{title}</Typography><Typography color="text.secondary">{subtitle}</Typography></Box>
    {action}
  </Stack>
}

export function EmptyState({ icon, title, action }: { icon: React.ReactNode; title: string; action?: React.ReactNode }) {
  return <Paper sx={{ py: 8, px: 2, textAlign: 'center' }}>
    <Avatar sx={{ mx: 'auto', mb: 2, bgcolor: 'action.selected', color: 'text.secondary' }}>{icon}</Avatar>
    <Typography variant="h6" gutterBottom>{title}</Typography>{action}
  </Paper>
}
