"""
Server Money - 認証システム

このファイルは、bcryptを使用したパスワード認証、セッション管理、
ログイン試行制限機能を提供します。
"""

import os
import bcrypt
from datetime import datetime, timedelta
from functools import wraps
from flask import session, request, jsonify, redirect, url_for

# ログイン試行回数制限のためのメモリ辞書
login_attempts = {}
LOGIN_ATTEMPT_LIMIT = 5
LOCKOUT_DURATION = 30  # 30分

def verify_password(password, hash_str):
    """パスワード検証
    
    Args:
        password (str): 入力されたパスワード
        hash_str (str): bcryptハッシュ化されたパスワード
        
    Returns:
        bool: パスワードが正しい場合True
    """
    try:
        password_bytes = password.encode('utf-8')
        hash_bytes = hash_str.encode('utf-8')
        return bcrypt.checkpw(password_bytes, hash_bytes)
    except Exception as e:
        from flask import current_app
        current_app.logger.error(f"パスワード検証エラー: {e}")
        return False

def is_ip_locked(ip_address):
    """IPアドレスがロックされているかチェック
    
    Args:
        ip_address (str): IPアドレス
        
    Returns:
        bool: ロックされている場合True
    """
    if ip_address in login_attempts:
        attempts, last_attempt = login_attempts[ip_address]
        if attempts >= LOGIN_ATTEMPT_LIMIT:
            time_since_last = datetime.now() - last_attempt
            if time_since_last.total_seconds() < LOCKOUT_DURATION * 60:
                return True
            else:
                # ロック期間が過ぎたらリセット
                del login_attempts[ip_address]
    return False

def record_login_attempt(ip_address, success=False):
    """ログイン試行を記録
    
    Args:
        ip_address (str): IPアドレス
        success (bool): ログイン成功時True
    """
    if success:
        # 成功時はリセット
        if ip_address in login_attempts:
            del login_attempts[ip_address]
    else:
        # 失敗時は回数をカウント
        if ip_address in login_attempts:
            attempts, _ = login_attempts[ip_address]
            login_attempts[ip_address] = (attempts + 1, datetime.now())
        else:
            login_attempts[ip_address] = (1, datetime.now())

def login_required(f):
    """ログイン必須デコレータ
    
    このデコレータを適用したエンドポイントは認証が必要になります。
    
    Args:
        f: 装飾する関数
        
    Returns:
        function: 装飾された関数
    """
    @wraps(f)
    def decorated_function(*args, **kwargs):
        if 'logged_in' not in session or not session['logged_in']:
            if request.is_json:
                return jsonify({'error': '認証が必要です', 'login_required': True}), 401
            return redirect(url_for('auth.login_page'))
        return f(*args, **kwargs)
    return decorated_function

def check_auth_setup():
    """認証設定の確認
    
    Returns:
        bool: 認証設定が完了している場合True
    """
    if not os.path.exists('.env'):
        print("\n⚠️  認証設定が見つかりません")
        print("初回セットアップを実行してください: uv run auth_setup.py")
        print("その後、アプリケーションを開始してください: uv run app.py")
        return False
    
    required_vars = ['LOGIN_USERNAME', 'LOGIN_PASSWORD_HASH', 'SECRET_KEY']
    missing_vars = []
    
    for var in required_vars:
        if not os.getenv(var):
            missing_vars.append(var)
    
    if missing_vars:
        print(f"\n❌ 以下の環境変数が設定されていません: {', '.join(missing_vars)}")
        print("認証設定を再実行してください: uv run auth_setup.py")
        return False
    
    return True

def get_client_ip():
    """クライアントIPアドレスを取得
    
    プロキシ環境も考慮してクライアントIPを取得します。
    
    Returns:
        str: IPアドレス
    """
    return request.environ.get('HTTP_X_FORWARDED_FOR', 
                              request.environ.get('REMOTE_ADDR', 'unknown'))

def get_remaining_attempts(ip_address):
    """残りログイン試行回数を取得
    
    Args:
        ip_address (str): IPアドレス
        
    Returns:
        int: 残り試行回数
    """
    if ip_address in login_attempts:
        attempts, _ = login_attempts[ip_address]
        return max(0, LOGIN_ATTEMPT_LIMIT - attempts)
    return LOGIN_ATTEMPT_LIMIT