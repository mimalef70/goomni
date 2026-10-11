import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { afterEach, expect, it, vi } from 'vitest'
import i18n from '../lib/i18n'
import { ServicePage } from './service'

afterEach(() => vi.unstubAllGlobals())

it('shows evidence scope and limits independently of provider availability', async () => {
  document.head.innerHTML = '<base href="http://localhost/gateway/ui/">'
  await i18n.changeLanguage('en')
  vi.stubGlobal(
    'fetch',
    vi.fn(async (url: unknown) => {
      const results = String(url).includes('app/providers')
        ? [
            {
              id: 'eitaa',
              name: 'Eitaa',
              enabled: false,
              verification: 'Scoped live evidence; see scope and limitations',
              evidence: {
                level: 'live_scoped',
                date: '2026-10-10',
                scope: 'Two synthetic fixture accounts; selected text round trips.',
                references: ['docs/providers/acceptance.md'],
                limitations: ['Multi-day recovery remains unverified.'],
              },
            },
          ]
        : { version: 'synthetic', status: 'ready', server_time: '' }
      return new Response(JSON.stringify({ results }))
    }),
  )
  const cache = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  render(
    <QueryClientProvider client={cache}>
      <ServicePage serverTime="" />
    </QueryClientProvider>,
  )
  expect(await screen.findByText('Scoped live evidence; see scope and limitations')).toBeVisible()
  expect(screen.getByText(i18n.t('providerUnavailable'))).toBeVisible()
  await userEvent.setup().click(screen.getByText(i18n.t('verificationEvidence')))
  expect(screen.getByText('2026-10-10')).toBeVisible()
  expect(
    screen.getByText('Two synthetic fixture accounts; selected text round trips.'),
  ).toBeVisible()
  expect(screen.getByText('Multi-day recovery remains unverified.')).toBeVisible()
})
