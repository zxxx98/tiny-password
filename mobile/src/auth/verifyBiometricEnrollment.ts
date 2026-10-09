import {SessionController} from './session';
import type {RememberedCredentials} from './rememberedLogin';

/** Verify enrollment in an isolated session without replacing the active vault session. */
export async function verifyBiometricEnrollment(
  serverUrl: string,
  credentials: RememberedCredentials,
  expectedUserId: string,
): Promise<void> {
  const verification = new SessionController();
  verification.setServerUrl(serverUrl);
  try {
    if (!await verification.preflight()) { throw new Error('无法连接服务器，请稍后重试'); }
    const result = await verification.login(credentials.username, credentials.password);
    if (!result.ok) { throw new Error(result.error || '密码验证失败'); }
    const snapshot = verification.getSnapshot();
    if (snapshot.phase !== 'authenticated' || snapshot.principal?.user_id !== expectedUserId) {
      throw new Error('请先完成当前账号的密码登录或强制改密');
    }
  } finally {
    await verification.signOut();
  }
}
