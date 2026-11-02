import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
  AlertDialogTrigger,
} from '@/components/ui/alert-dialog'
import { Button } from '@/components/ui/button'
import { Checkbox } from '@/components/ui/checkbox'
import { mapApiError } from '@/lib/api/errors'
import {
  clearSemanticIndex,
  fetchSemanticIndexStatus,
  rebuildSemanticIndex,
  setSemanticIndexPaused,
  type SemanticIndexState,
  type SemanticIndexStatus,
} from '@/lib/api/semanticIndex'
// House icon set, chosen for consistency with the rest of the application: each
// glyph is the control's action (rebuild, delete, busy).
import { Loader2, RefreshCw, Trash2 } from 'lucide-react'
import { useCallback, useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { useSetTitle } from '@/features/viewer/setTitle'

const POLL_INTERVAL_MS = 2000

const pillClass = (state: SemanticIndexState): string => {
  switch (state) {
    case 'fresh':
      return 'settings__pill settings__pill-success text-success'
    case 'stale':
      return 'settings__pill settings__pill-warning text-warning'
    case 'failed':
      return 'settings__pill settings__pill-error'
    default:
      return 'settings__role-pill settings__role-pill--default'
  }
}

export default function SemanticIndexSettings() {
  const { t } = useTranslation('semanticIndex')
  useSetTitle({ title: t('pageTitle') })

  const [status, setStatus] = useState<SemanticIndexStatus | null>(null)
  const [isLoading, setIsLoading] = useState(true)
  const [statusError, setStatusError] = useState('')
  const [saving, setSaving] = useState(false)
  const [working, setWorking] = useState(false)

  const load = useCallback(async () => {
    try {
      setStatus(await fetchSemanticIndexStatus())
      setStatusError('')
    } catch (err) {
      setStatusError(mapApiError(err, t('loadError')).message)
    } finally {
      setIsLoading(false)
    }
  }, [t])

  useEffect(() => {
    void load()
  }, [load])

  // Only a running build needs polling; everything else settles on its own.
  useEffect(() => {
    if (status?.state !== 'running') return
    const timer = window.setInterval(() => {
      void load()
    }, POLL_INTERVAL_MS)
    return () => window.clearInterval(timer)
  }, [status?.state, load])

  const applyPaused = async (paused: boolean) => {
    setSaving(true)
    try {
      await setSemanticIndexPaused(paused)
      toast.success(paused ? t('pausedToast') : t('resumedToast'))
      await load()
    } catch (err) {
      toast.error(mapApiError(err, t('updateError')).message)
    } finally {
      setSaving(false)
    }
  }

  const rebuild = async () => {
    setWorking(true)
    try {
      await rebuildSemanticIndex()
      toast.success(t('rebuildToast'))
      await load()
    } catch (err) {
      toast.error(mapApiError(err, t('rebuildError')).message)
      await load()
    } finally {
      setWorking(false)
    }
  }

  const clearAndRebuild = async () => {
    setWorking(true)
    try {
      await clearSemanticIndex()
      toast.success(t('clearToast'))
      await load()
    } catch (err) {
      toast.error(mapApiError(err, t('clearError')).message)
      await load()
    } finally {
      setWorking(false)
    }
  }

  const disabled = status?.state === 'off'
  const building = status?.state === 'running'
  const stateLabel: Record<SemanticIndexState, string> = {
    off: t('stateOff'),
    missing: t('stateMissing'),
    queued: t('stateQueued'),
    running: t('stateRunning'),
    stale: t('stateStale'),
    failed: t('stateFailed'),
    fresh: t('stateFresh'),
  }

  return (
    <div className="settings" data-testid="semantic-index-settings">
      <h1 className="settings__title">{t('pageTitle')}</h1>

      {statusError && (
        <div className="settings__section">
          <p
            className="text-error text-sm"
            data-testid="semantic-index-status-error"
          >
            {statusError}
          </p>
        </div>
      )}

      <div className="settings__section">
        <h2 className="settings__section-title">{t('statusSectionTitle')}</h2>
        <p className="settings__section-description">
          {t('statusSectionDescription')}
        </p>

        <div className="settings__preview">
          <span className="settings__preview-label">{t('stateLabel')}</span>
          {isLoading ? (
            <Loader2 className="text-muted h-4 w-4 animate-spin" />
          ) : (
            <span
              className={pillClass(status?.state ?? 'off')}
              data-testid="semantic-index-state"
            >
              {stateLabel[status?.state ?? 'off']}
            </span>
          )}
          {building && <Loader2 className="text-muted h-4 w-4 animate-spin" />}
        </div>

        {!isLoading && status && status.state !== 'off' && (
          <>
            <div className="settings__preview">
              <span className="settings__preview-label">{t('modelLabel')}</span>
              <span className="text-interface-text text-sm">
                {status.model_id || t('unknown')}
              </span>
            </div>

            <div className="settings__preview">
              <span className="settings__preview-label">
                {t('pendingLabel')}
              </span>
              <span className="text-interface-text text-sm">
                {t('pendingValue', { count: status.pending })}
              </span>
            </div>

            {status.last_error && (
              <div className="settings__preview border-error/20 bg-error/5">
                <span className="settings__preview-label">
                  {t('lastErrorLabel')}
                </span>
                <span className="text-error text-sm">{status.last_error}</span>
              </div>
            )}
          </>
        )}

        {status?.state === 'missing' && (
          <p className="settings__hint">{t('missingHint')}</p>
        )}

        {disabled && (
          <div
            className="settings__notice"
            data-testid="semantic-index-disabled"
          >
            <strong>{t('disabledTitle')}</strong>
            <span>{t('disabledDescription')}</span>
          </div>
        )}
      </div>

      <div className="settings__section">
        <h2 className="settings__section-title">{t('autoSectionTitle')}</h2>

        <div className="settings__field">
          <label className="settings__checkbox-label">
            <Checkbox
              data-testid="semantic-index-auto-checkbox"
              checked={!!status && !status.paused}
              disabled={isLoading || saving || disabled}
              onCheckedChange={(checked) => {
                if (!status || !!checked === !status.paused) return
                void applyPaused(!checked)
              }}
            />
            {saving && <Loader2 className="mr-2 h-4 w-4 animate-spin" />}
            {t('autoCheckboxLabel')}
          </label>
          <p className="settings__section-description">
            {t('autoCheckboxDescription')}
          </p>
        </div>
      </div>

      <div className="settings__section">
        <h2 className="settings__section-title">
          {t('maintenanceSectionTitle')}
        </h2>

        <div className="settings__field">
          <p className="settings__section-description">
            {t('rebuildDescription')}
          </p>
          <Button
            variant="outline"
            disabled={isLoading || working || building || disabled}
            onClick={() => void rebuild()}
            data-testid="semantic-index-rebuild"
          >
            {working || building ? (
              <Loader2 className="mr-2 h-4 w-4 animate-spin" />
            ) : (
              <RefreshCw className="mr-2 h-4 w-4" />
            )}
            {building ? t('rebuilding') : t('rebuildButton')}
          </Button>
        </div>

        <div className="settings__field">
          <p className="settings__section-description">
            {t('clearDescription')}
          </p>
          <AlertDialog>
            <AlertDialogTrigger asChild>
              <Button
                variant="destructive"
                disabled={isLoading || working || disabled}
                data-testid="semantic-index-clear"
              >
                <Trash2 className="mr-2 h-4 w-4" />
                {t('clearButton')}
              </Button>
            </AlertDialogTrigger>
            <AlertDialogContent>
              <AlertDialogHeader>
                <AlertDialogTitle>{t('clearConfirmTitle')}</AlertDialogTitle>
                <AlertDialogDescription>
                  {t('clearConfirmDescription')}
                </AlertDialogDescription>
              </AlertDialogHeader>
              <AlertDialogFooter>
                <AlertDialogCancel>{t('cancel')}</AlertDialogCancel>
                <AlertDialogAction onClick={() => void clearAndRebuild()}>
                  {t('clearConfirmAction')}
                </AlertDialogAction>
              </AlertDialogFooter>
            </AlertDialogContent>
          </AlertDialog>
        </div>
      </div>
    </div>
  )
}
