"""
Server Money - 認証関連ルート

このファイルは、ログイン、ログアウト、認証状態確認の
エンドポイントを定義します。
"""

import os
from datetime import datetime
from flask import Blueprint, render_template, request, jsonify, session, redirect, url_for
from auth import (
    verify_password, is_ip_locked, record_login_attempt, 
    login_required, get_client_ip, get_remaining_attempts,
    LOCKOUT_DURATION
)

# Blueprintの作成
auth_bp = Blueprint('auth', __name__)

@auth_bp.route("/login")
def login_page():
    """ログインページ"""
    if 'logged_in' in session and session['logged_in']:
        return redirect(url_for('main.hello_world'))
    return render_template('login.html')

@auth_bp.route("/api/login", methods=['POST'])
def login():
    """ログインAPI"""
    from flask import current_app
    
    try:
        data = request.get_json()
        if not data:
            return jsonify({'error': 'リクエストデータが無効です'}), 400
        
        username = data.get('username', '').strip()
        password = data.get('password', '')
        ip_address = get_client_ip()
        
        # IPアドレスロックチェック
        if is_ip_locked(ip_address):
            current_app.logger.warning(f"ロックされたIPからのログイン試行: {ip_address}")
            return jsonify({
                'error': f'ログイン試行回数が上限に達しました。{LOCKOUT_DURATION}分後に再試行してください。'
            }), 429
        
        # 認証情報の検証
        expected_username = os.getenv('LOGIN_USERNAME')
        expected_password_hash = os.getenv('LOGIN_PASSWORD_HASH')
        
        if not expected_username or not expected_password_hash:
            current_app.logger.error("認証設定が不完全です")
            return jsonify({'error': 'サーバー設定エラー'}), 500
        
        # ユーザー名とパスワードの検証
        if username == expected_username and verify_password(password, expected_password_hash):
            # ログイン成功
            session['logged_in'] = True
            session['username'] = username
            session['login_time'] = datetime.now().isoformat()
            session.permanent = True
            
            record_login_attempt(ip_address, success=True)
            current_app.logger.info(f"ユーザー '{username}' がログインしました (IP: {ip_address})")
            
            return jsonify({
                'success': True,
                'message': 'ログインしました',
                'redirect': url_for('main.hello_world')
            })
        else:
            # ログイン失敗
            record_login_attempt(ip_address, success=False)
            current_app.logger.warning(f"ログイン失敗: ユーザー '{username}' (IP: {ip_address})")
            
            remaining_attempts = get_remaining_attempts(ip_address)
            return jsonify({
                'error': 'ユーザー名またはパスワードが正しくありません',
                'remaining_attempts': remaining_attempts
            }), 401
            
    except Exception as e:
        current_app.logger.error(f"ログイン処理エラー: {str(e)}", exc_info=True)
        return jsonify({'error': 'ログイン処理に失敗しました'}), 500

@auth_bp.route("/api/logout", methods=['POST'])
@login_required
def logout():
    """ログアウトAPI"""
    from flask import current_app
    
    username = session.get('username', 'unknown')
    session.clear()
    current_app.logger.info(f"ユーザー '{username}' がログアウトしました")
    return jsonify({'success': True, 'message': 'ログアウトしました'})

@auth_bp.route("/api/auth_status")
def auth_status():
    """認証状態確認API"""
    if 'logged_in' in session and session['logged_in']:
        return jsonify({
            'authenticated': True,
            'username': session.get('username'),
            'login_time': session.get('login_time')
        })
    return jsonify({'authenticated': False})