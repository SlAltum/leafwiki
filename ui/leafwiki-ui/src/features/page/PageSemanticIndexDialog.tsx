import BaseDialog from '@/components/BaseDialog'
import { Button } from '@/components/ui/button'
import { mapApiError } from '@/lib/api/errors'
import {
  fetchPageIndexStatus,
  rebuildPageIndex,
  type SemanticPageState,
  type SemanticPageStatus,
} from '@/lib/api/semanticIndex'
import { DIALOG_PAGE_SEMANTIC_INDEX } from '@/lib/registries'
// House icon set, chosen for consistency with the rest of the application.
import { Loader2, RefreshCw } from 'lucide-react'
import { useCallback, useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'

type PageSemanticIndexDialogProps = {
  page: { id: string; title: string }
}

export function PageSemanticIndexDialog({
  page,
}: PageSemanticIndexDialogProps) {
  const { t } = useTranslation('semanticIndex')
  const [status, setStatus] = useState<SemanticPageStatus | null>(null)
  const [isLoading, setIsLoading] = useState(true)
  const [isRebuilding, setIsRebuilding] = useState(false)
  const [error, setError] = useState('')

  const load = useCallback(async () => {
    try {
      setStatus(await fetchPageIndexStatus(page.id))
      setError('')
    } catch (err) {
      setError(mapApiError(err, t('pageDialog.loadError')).message)
    } finally {
      setIsLoading(false)
    }
  }, [page.id, t])

  useEffect(() => {
    void load()
  }, [load])

  const rebuild = async () => {
    setIsRebuilding(true)
    try {
      await rebuildPageIndex(page.id)
      toast.success(t('pageDialog.rebuildToast'))
      await load()
    } catch (err) {
      toast.error(mapApiError(err, t('pageDialog.rebuildError')).message)
    } finally {
      setIsRebuilding(false)
    }
  }

  const labels: Record<SemanticPageState, string> = {
    off: t('pageDialog.stateOff'),
    indexed: t('pageDialog.stateIndexed'),
    stale: t('pageDialog.stateStale'),
    queued: t('pageDialog.stateQueued'),
    failed: t('pageDialog.stateFailed'),
    missing: t('pageDialog.stateMissing'),
  }

  const hints: Partial<Record<SemanticPageState, string>> = {
    off: t('pageDialog.hintOff'),
    stale: t('pageDialog.hintStale'),
    missing: t('pageDialog.hintMissing'),
    queued: t('pageDialog.hintQueued'),
    failed: t('pageDialog.hintFailed'),
  }

  const canRebuild = !isLoading && !isRebuilding && status?.state !== 'off'

  return (
    <BaseDialog
      dialogTitle={t('pageDialog.title')}
      dialogDescription={t('pageDialog.description', { title: page.title })}
      dialogType={DIALOG_PAGE_SEMANTIC_INDEX}
      onClose={() => true}
      onConfirm={async () => false}
      testidPrefix="page-semantic-index-dialog"
      cancelButton={{ label: t('pageDialog.close'), variant: 'outline' }}
    >
      <div className="space-y-4" data-testid="page-semantic-index-body">
        {error && <p className="text-error text-sm">{error}</p>}

        <div className="flex items-center gap-2">
          <span className="text-muted text-sm">
            {t('pageDialog.stateLabel')}
          </span>
          {isLoading ? (
            <Loader2 className="text-muted h-4 w-4 animate-spin" />
          ) : (
            <span data-testid="page-semantic-index-state">
              {labels[status?.state ?? 'off']}
            </span>
          )}
        </div>

        {status && status.state !== 'off' && (
          <div className="flex items-center gap-2">
            <span className="text-muted text-sm">
              {t('pageDialog.passagesLabel')}
            </span>
            <span data-testid="page-semantic-index-passages">
              {t('pageDialog.passagesValue', { count: status.passages })}
            </span>
          </div>
        )}

        {status && hints[status.state] && (
          <p className="text-muted text-sm">{hints[status.state]}</p>
        )}

        {status?.last_error && (
          <p className="text-error text-sm">{status.last_error}</p>
        )}

        <div className="flex justify-end">
          <Button
            variant="outline"
            disabled={!canRebuild}
            onClick={() => void rebuild()}
            data-testid="page-semantic-index-rebuild"
          >
            {isRebuilding ? (
              <Loader2 className="mr-2 h-4 w-4 animate-spin" />
            ) : (
              <RefreshCw className="mr-2 h-4 w-4" />
            )}
            {isRebuilding
              ? t('pageDialog.rebuilding')
              : t('pageDialog.rebuildButton')}
          </Button>
        </div>
      </div>
    </BaseDialog>
  )
}
