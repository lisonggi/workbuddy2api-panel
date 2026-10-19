// 导出凭证的动作（多选操作条和详情抽屉共用）：下载文件并提示妥善保管。文件格式见 lib/credentials。
import { useState } from 'react'
import { toast } from '@heroui/react'
import { exportCredentials } from '../../lib/credentials'
import { toastError } from '../../lib/hooks'

export function useExportCredentials() {
  const [busy, setBusy] = useState(false)
  const run = async (uids: string[]) => {
    if (uids.length === 0) return
    setBusy(true)
    try {
      const n = await exportCredentials(uids)
      toast.success(`已导出 ${n} 个账号的凭证`, { description: '文件里是能直接登录的凭证，请妥善保管，不要发给别人。', timeout: 6000 })
    } catch (e) {
      toastError(e, '导出失败：')
    } finally {
      setBusy(false)
    }
  }
  return { busy, run }
}
