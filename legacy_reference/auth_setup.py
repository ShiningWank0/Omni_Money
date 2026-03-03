"""
Server Money - 認証セットアップユーティリティ

初回起動時の認証設定を自動化するためのスクリプト
"""
import os
import secrets
import bcrypt
from getpass import getpass


def generate_secret_key():
    """セキュアなSECRET_KEYを生成"""
    return secrets.token_hex(32)


def hash_password(password):
    """パスワードをbcryptでハッシュ化"""
    # パスワードをbytes型に変換
    password_bytes = password.encode('utf-8')
    # saltを生成してハッシュ化
    salt = bcrypt.gensalt()
    hashed = bcrypt.hashpw(password_bytes, salt)
    return hashed.decode('utf-8')


def create_env_file():
    """初回起動時に.envファイルを作成"""
    env_file_path = '.env'
    
    # 既に.envファイルが存在する場合は何もしない
    if os.path.exists(env_file_path):
        print("✓ .envファイルが既に存在します")
        return False
    
    print("=== Server Money 初回認証設定 ===")
    print("安全なログイン認証を設定します。")
    print()
    
    # ユーザー名の入力
    username = input("ログインID（デフォルト: admin）: ").strip()
    if not username:
        username = "admin"
    
    # パスワードの入力
    while True:
        password = getpass("パスワード: ")
        if len(password) < 8:
            print("❌ パスワードは8文字以上にしてください")
            continue
        
        password_confirm = getpass("パスワード再入力: ")
        if password != password_confirm:
            print("❌ パスワードが一致しません")
            continue
        
        break
    
    # パスワードハッシュ化
    print("パスワードをハッシュ化しています...")
    password_hash = hash_password(password)
    
    # ホストIP設定
    print()
    print("=== サーバーホストIP設定 ===")
    print("⚠️  セキュリティ警告:")
    print("   - 0.0.0.0: 全てのIPアドレスからアクセス可能（リスクあり）")
    print("   - 127.0.0.1: ローカルホストのみアクセス可能（推奨）")
    print("   - 特定IP: 指定したIPアドレスのみアクセス可能（高セキュリティ）")
    print()
    
    while True:
        host_ip = input("ホストIPアドレス（デフォルト: 127.0.0.1）: ").strip()
        if not host_ip:
            host_ip = "127.0.0.1"
        
        if host_ip == "0.0.0.0":
            print("⚠️  注意: 0.0.0.0 を選択しました。")
            print("   これにより、ネットワーク上の全てのデバイスからアクセス可能になります。")
            print("   家計簿データが含まれるため、セキュリティリスクがあります。")
            confirm = input("本当に続行しますか？ (y/N): ").strip().lower()
            if confirm not in ['y', 'yes']:
                continue
        
        # 簡単なIP形式チェック
        ip_parts = host_ip.split('.')
        if len(ip_parts) == 4:
            try:
                for part in ip_parts:
                    if not (0 <= int(part) <= 255):
                        raise ValueError
                break
            except ValueError:
                print("❌ 無効なIPアドレス形式です")
                continue
        else:
            print("❌ 無効なIPアドレス形式です")
            continue
    
    print(f"ホストIP: {host_ip} に設定しました")
    
    # SECRET_KEY生成
    secret_key = generate_secret_key()
    
    # .envファイルの内容を作成
    env_content = f"""# Server Money 認証設定（自動生成）
# このファイルは機密情報を含むため、絶対に共有しないでください

# ログイン認証情報
LOGIN_USERNAME={username}
LOGIN_PASSWORD_HASH={password_hash}

# セッション設定
SECRET_KEY={secret_key}

# サーバー設定
HOST_IP={host_ip}

# アプリケーション設定
ENVIRONMENT=production
LOG_LEVEL=INFO
"""
    
    # .envファイルを作成
    with open(env_file_path, 'w', encoding='utf-8') as f:
        f.write(env_content)
    
    # ファイルのパーミッションを制限（Unix系のみ）
    if os.name != 'nt':  # Windows以外
        os.chmod(env_file_path, 0o600)  # 所有者のみ読み書き可能
    
    print()
    print("✅ 認証設定が完了しました！")
    print(f"   ログインID: {username}")
    print("   パスワード: [設定済み]")
    print()
    print("⚠️  重要: .envファイルは機密情報を含むため、以下に注意してください：")
    print("   - バックアップを取る場合は安全な場所に保管")
    print("   - 他の人と共有しない")
    print("   - Gitにコミットしない（.gitignoreに追加済み）")
    print()
    
    return True


def verify_password(password, hash_str):
    """パスワード検証のテスト用関数"""
    password_bytes = password.encode('utf-8')
    hash_bytes = hash_str.encode('utf-8')
    return bcrypt.checkpw(password_bytes, hash_bytes)


if __name__ == "__main__":
    try:
        created = create_env_file()
        if created:
            print("Server Moneyアプリケーションを開始してください。")
            print("コマンド: uv run app.py")
        else:
            print("既存の認証設定を使用します。")
    except KeyboardInterrupt:
        print("\n\n設定がキャンセルされました。")
    except Exception as e:
        print(f"\n❌ エラーが発生しました: {e}")
        print("設定を再試行してください。")