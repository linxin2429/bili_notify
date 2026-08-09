import { isRouteErrorResponse, useRouteError } from 'react-router-dom'
import { Alert, Button } from '../shared/ui'

export function RouteErrorBoundary() {
  const error = useRouteError()
  const message = isRouteErrorResponse(error) ? `${error.status} ${error.statusText}` : error instanceof Error ? error.message : '页面发生未知错误'
  return <main className="bootstrap"><Alert tone="danger"><h1>页面无法显示</h1><p>{message}</p><Button variant="primary" onPress={() => location.reload()}>重新加载</Button></Alert></main>
}
