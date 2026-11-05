// Translations of sign-in with passkeys, kept apart so the feature's strings
// sit together. Merged into ru / zh in lib/i18n.ts.
import type { Dict } from "@/lib/i18n";

export const passkeyRu: Dict = {
  "The panel is opened by IP address. Passkeys are bound to a domain name and browsers do not allow them on an IP address. Open the panel by its domain over HTTPS to use them; the password and the app code work everywhere.":
    "Панель открыта по IP-адресу. Passkey привязывается к доменному имени, и браузеры не разрешают его на IP. Чтобы пользоваться passkey, откройте панель по домену через HTTPS; пароль и код из Google Authenticator работают везде.",
  "The panel is opened over plain HTTP. Browsers allow passkeys only over HTTPS (or on localhost). Put the panel behind HTTPS on a domain to use them.":
    "Панель открыта по обычному HTTP. Браузеры разрешают passkey только через HTTPS (или на localhost). Чтобы пользоваться passkey, откройте панель по домену через HTTPS.",
  "This browser does not support passkeys.": "Этот браузер не поддерживает passkey.",
  "This address cannot be used for passkeys: open the panel by a full domain name.":
    "Этот адрес не подходит для passkey: откройте панель по полному доменному имени.",
  "The browser did not say which address the panel is opened at: passkeys cannot be checked.":
    "Браузер не сообщил, по какому адресу открыта панель: проверить passkey нельзя.",
  "the passkey was not accepted": "passkey не принят",
  "the passkey prompt timed out, try again": "время запроса passkey истекло, попробуйте ещё раз",
  "this passkey is already added": "этот passkey уже добавлен",
  "too many passkeys: remove one first": "слишком много passkey: сначала удалите один",
  "the panel address changed during the prompt, try again": "адрес панели изменился во время запроса, попробуйте ещё раз",
  "passkey added: the sign-in page now offers it at {host}": "passkey добавлен: страница входа теперь предлагает его на {host}",
  "opened by a domain name, not an IP address ({host})": "открыта по доменному имени, а не по IP ({host})",
  "HTTPS (or localhost)": "HTTPS (или localhost)",
  "the browser supports passkeys": "браузер поддерживает passkey",
  "this device has Face ID / Touch ID / Windows Hello": "на этом устройстве есть Face ID / Touch ID / Windows Hello",
  "no built-in Face ID / Touch ID / Windows Hello here: a phone or a security key can still be used":
    "встроенного Face ID / Touch ID / Windows Hello здесь нет: можно использовать телефон или аппаратный ключ",
  "passkeys · face id / touch id": "passkey · face id / touch id",
  "on at this address": "включено на этом адресе",
  "Optional. Sign in with Face ID, Touch ID, Windows Hello, a phone or a security key instead of the password and the app code. The password and the app code keep working as a fallback.":
    "Необязательно. Вход по Face ID, Touch ID, Windows Hello, с телефона или аппаратного ключа вместо пароля и кода из Google Authenticator. Пароль и код из Google Authenticator остаются запасным способом.",
  "check of this address": "проверка этого адреса",
  "I understand: the passkey works only at {host}. If the panel moves to another address, I add a new passkey there; the password and the app code stay as a fallback.":
    "Понимаю: passkey работает только на {host}. Если панель переедет на другой адрес, я добавлю там новый passkey; пароль и код из Google Authenticator остаются запасным способом.",
  "add another passkey": "добавить ещё passkey",
  "turn on: add a passkey": "включить: добавить passkey",
  "another address: not offered here": "другой адрес: здесь не предлагается",
  "added {when}": "добавлен {when}",
  "last used {when}": "последний вход {when}",
  "remove": "удалить",
  "remove passkey": "удалить passkey",
  "no passkeys for this address: sign in with the password": "для этого адреса нет passkey: войдите по паролю",
  "sign in with a passkey": "войти по passkey",
  "Face ID, Touch ID, Windows Hello or a security key. Or use the password and the app code below.":
    "Face ID, Touch ID, Windows Hello или аппаратный ключ. Или войдите по паролю и коду из Google Authenticator ниже.",
  "cancelled; enter a new code from the authenticator app to try again":
    "отменено; чтобы повторить, введите новый код из Google Authenticator",
  "passkey name (to tell your devices apart)": "название passkey (чтобы различать устройства)",
  "1. Confirm it is you: the current 6-digit code from Google Authenticator (the app you sign in with)":
    "1. Подтвердите, что это вы: текущий 6-значный код из Google Authenticator (того же приложения, что при входе)",
  "code from Google Authenticator": "код из Google Authenticator",
  "confirm code": "подтвердить код",
  "tick the box above first": "сначала отметьте галочку выше",
  "code confirmed": "код подтверждён",
  "2. Press the button: the browser asks for Touch ID, Face ID or the device PIN":
    "2. Нажмите кнопку: браузер попросит Touch ID, Face ID или PIN устройства",
  "add passkey · Touch ID": "добавить passkey · Touch ID",
};

export const passkeyZh: Dict = {
  "The panel is opened by IP address. Passkeys are bound to a domain name and browsers do not allow them on an IP address. Open the panel by its domain over HTTPS to use them; the password and the app code work everywhere.":
    "面板是通过 IP 地址打开的。通行密钥绑定到域名，浏览器不允许在 IP 地址上使用。请通过 HTTPS 域名打开面板以使用通行密钥；密码和 Google Authenticator 验证码在任何地址都可用。",
  "The panel is opened over plain HTTP. Browsers allow passkeys only over HTTPS (or on localhost). Put the panel behind HTTPS on a domain to use them.":
    "面板是通过普通 HTTP 打开的。浏览器只允许在 HTTPS（或 localhost）上使用通行密钥。请为面板配置域名和 HTTPS 后再使用。",
  "This browser does not support passkeys.": "此浏览器不支持通行密钥。",
  "This address cannot be used for passkeys: open the panel by a full domain name.": "此地址无法使用通行密钥：请通过完整域名打开面板。",
  "The browser did not say which address the panel is opened at: passkeys cannot be checked.": "浏览器未提供面板的访问地址，无法检查通行密钥。",
  "the passkey was not accepted": "通行密钥未被接受",
  "the passkey prompt timed out, try again": "通行密钥请求已超时，请重试",
  "this passkey is already added": "此通行密钥已添加",
  "too many passkeys: remove one first": "通行密钥过多：请先删除一个",
  "the panel address changed during the prompt, try again": "请求期间面板地址发生变化，请重试",
  "passkey added: the sign-in page now offers it at {host}": "通行密钥已添加：登录页面现在会在 {host} 上提供它",
  "opened by a domain name, not an IP address ({host})": "通过域名而非 IP 地址打开（{host}）",
  "HTTPS (or localhost)": "HTTPS（或 localhost）",
  "the browser supports passkeys": "浏览器支持通行密钥",
  "this device has Face ID / Touch ID / Windows Hello": "此设备支持 Face ID / Touch ID / Windows Hello",
  "no built-in Face ID / Touch ID / Windows Hello here: a phone or a security key can still be used":
    "此设备没有内置的 Face ID / Touch ID / Windows Hello：仍可使用手机或安全密钥",
  "passkeys · face id / touch id": "通行密钥 · face id / touch id",
  "on at this address": "已在此地址启用",
  "Optional. Sign in with Face ID, Touch ID, Windows Hello, a phone or a security key instead of the password and the app code. The password and the app code keep working as a fallback.":
    "可选。使用 Face ID、Touch ID、Windows Hello、手机或安全密钥登录，代替密码和 Google Authenticator 验证码。密码和 Google Authenticator 验证码仍可作为备用方式。",
  "check of this address": "检查此地址",
  "I understand: the passkey works only at {host}. If the panel moves to another address, I add a new passkey there; the password and the app code stay as a fallback.":
    "我了解：此通行密钥仅在 {host} 上有效。如果面板迁移到其他地址，我会在那里添加新的通行密钥；密码和 Google Authenticator 验证码仍可作为备用方式。",
  "add another passkey": "再添加一个通行密钥",
  "turn on: add a passkey": "启用：添加通行密钥",
  "another address: not offered here": "其他地址：此处不提供",
  "added {when}": "添加于 {when}",
  "last used {when}": "上次使用 {when}",
  "remove": "删除",
  "remove passkey": "删除通行密钥",
  "no passkeys for this address: sign in with the password": "此地址没有通行密钥：请使用密码登录",
  "sign in with a passkey": "使用通行密钥登录",
  "Face ID, Touch ID, Windows Hello or a security key. Or use the password and the app code below.":
    "Face ID、Touch ID、Windows Hello 或安全密钥。也可以使用下方的密码和 Google Authenticator 验证码。",
  "cancelled; enter a new code from the authenticator app to try again": "已取消；请输入 Google Authenticator 中的新验证码重试",
  "passkey name (to tell your devices apart)": "通行密钥名称（用于区分设备）",
  "1. Confirm it is you: the current 6-digit code from Google Authenticator (the app you sign in with)":
    "1. 确认身份：输入 Google Authenticator（登录时使用的应用）中当前的 6 位验证码",
  "code from Google Authenticator": "Google Authenticator 验证码",
  "confirm code": "确认验证码",
  "tick the box above first": "请先勾选上方的选项",
  "code confirmed": "验证码已确认",
  "2. Press the button: the browser asks for Touch ID, Face ID or the device PIN":
    "2. 点击按钮：浏览器会请求 Touch ID、Face ID 或设备 PIN",
  "add passkey · Touch ID": "添加通行密钥 · Touch ID",
};
